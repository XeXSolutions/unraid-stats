package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

type NetworkStats struct {
	BytesSent  uint64    `json:"bytes_sent"`
	BytesRecv  uint64    `json:"bytes_recv"`
	Timestamp  time.Time `json:"timestamp"`
	Interfaces []string  `json:"interfaces"`
}

type ArrayStatus struct {
	State         string `json:"state"`
	Protection    string `json:"protection"`
	TotalCapacity uint64 `json:"total_capacity"`
	UsedSpace     uint64 `json:"used_space"`
	CacheSize     uint64 `json:"cache_size"`
	ParitySize    uint64 `json:"parity_size"`
}

type DiskInfo struct {
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"used_percent"`
	Type        string  `json:"type"` // "parity", "data", "cache", or "pool"
}

type SystemStats struct {
	Hostname     string                 `json:"hostname"`
	CPUUsage     []float64              `json:"cpu_usage"`
	CPUTemp      float64                `json:"cpu_temp"`
	CPUCores     int                    `json:"cpu_cores"`
	LoadAverage  []float64              `json:"load_average"`
	MemoryStats  *mem.VirtualMemoryStat `json:"memory_stats"`
	NetworkStats *NetworkStats          `json:"network_stats"`
	ArrayStatus  *ArrayStatus           `json:"array_status"`
	DiskStats    []DiskInfo             `json:"disk_stats"`
	Uptime       time.Duration          `json:"uptime"`
	Platform     string                 `json:"platform"`
}

func readUnraidConfig() (*ArrayStatus, []DiskInfo, error) {
	// Read array status from /proc/mdcmd
	mdcmdPath := "/host/proc/mdcmd"
	if _, err := os.Stat(mdcmdPath); os.IsNotExist(err) {
		mdcmdPath = "/proc/mdcmd" // Fallback for testing
	}

	mdcmdData, err := ioutil.ReadFile(mdcmdPath)
	if err != nil {
		return nil, nil, err
	}

	arrayStatus := &ArrayStatus{
		State: "Unknown",
	}

	// Parse mdcmd output for array status
	lines := strings.Split(string(mdcmdData), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "mdState=") {
			arrayStatus.State = strings.TrimPrefix(line, "mdState=")
		} else if strings.HasPrefix(line, "mdNumProtected=") {
			protected, _ := strconv.Atoi(strings.TrimPrefix(line, "mdNumProtected="))
			if protected > 0 {
				arrayStatus.Protection = "Protected"
			} else {
				arrayStatus.Protection = "Not Protected"
			}
		}
	}

	// Read disk information
	var diskInfos []DiskInfo

	// Read array disks from /dev/disk/by-id
	disksPath := "/host/dev/disk/by-id"
	if _, err := os.Stat(disksPath); os.IsNotExist(err) {
		disksPath = "/dev/disk/by-id" // Fallback for testing
	}

	files, err := ioutil.ReadDir(disksPath)
	if err != nil {
		return arrayStatus, nil, err
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), "ata-") || strings.HasPrefix(file.Name(), "scsi-") {
			diskPath := filepath.Join(disksPath, file.Name())
			realPath, err := filepath.EvalSymlinks(diskPath)
			if err != nil {
				continue
			}

			usage, err := disk.Usage(realPath)
			if err != nil {
				continue
			}

			diskType := "data"
			if strings.Contains(file.Name(), "parity") {
				diskType = "parity"
				arrayStatus.ParitySize = usage.Total
			} else if strings.Contains(file.Name(), "cache") {
				diskType = "cache"
				arrayStatus.CacheSize = usage.Total
			}

			diskInfo := DiskInfo{
				Path:        realPath,
				Name:        file.Name(),
				Total:       usage.Total,
				Used:        usage.Used,
				Free:        usage.Free,
				UsedPercent: usage.UsedPercent,
				Type:        diskType,
			}

			diskInfos = append(diskInfos, diskInfo)

			if diskType == "data" {
				arrayStatus.TotalCapacity += usage.Total
				arrayStatus.UsedSpace += usage.Used
			}
		}
	}

	return arrayStatus, diskInfos, nil
}

func getSystemStats() (*SystemStats, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	cpuPercent, err := cpu.Percent(time.Second, true)
	if err != nil {
		return nil, err
	}

	memStats, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	// Get CPU temperature
	var cpuTemp float64
	tempPath := "/host/sys/class/thermal/thermal_zone0/temp"
	if _, err := os.Stat(tempPath); os.IsNotExist(err) {
		tempPath = "/sys/class/thermal/thermal_zone0/temp"
	}
	if tempData, err := ioutil.ReadFile(tempPath); err == nil {
		if temp, err := strconv.ParseFloat(strings.TrimSpace(string(tempData)), 64); err == nil {
			cpuTemp = temp / 1000.0 // Convert from millidegrees to degrees
		}
	}

	// Get network stats
	netStats := &NetworkStats{
		Timestamp: time.Now(),
	}
	if counters, err := net.IOCounters(true); err == nil {
		for _, counter := range counters {
			if !strings.HasPrefix(counter.Name, "lo") { // Skip loopback
				netStats.BytesSent += counter.BytesSent
				netStats.BytesRecv += counter.BytesRecv
				netStats.Interfaces = append(netStats.Interfaces, counter.Name)
			}
		}
	}

	// Get CPU cores and load average
	cpuCores := 0
	if cores, err := cpu.Counts(true); err == nil {
		cpuCores = cores
	}

	loadAvg, err := load.Avg()
	var loadAvgSlice []float64
	if err == nil {
		loadAvgSlice = []float64{loadAvg.Load1, loadAvg.Load5, loadAvg.Load15}
	}

	// Get Unraid array status and disk info
	arrayStatus, diskInfos, err := readUnraidConfig()
	if err != nil {
		log.Printf("Warning: Could not read Unraid config: %v", err)
	}

	hostInfo, err := host.Info()
	if err != nil {
		return nil, err
	}

	return &SystemStats{
		Hostname:     hostname,
		CPUUsage:     cpuPercent,
		CPUTemp:      cpuTemp,
		CPUCores:     cpuCores,
		LoadAverage:  loadAvgSlice,
		MemoryStats:  memStats,
		NetworkStats: netStats,
		ArrayStatus:  arrayStatus,
		DiskStats:    diskInfos,
		Uptime:       time.Duration(hostInfo.Uptime) * time.Second,
		Platform:     hostInfo.Platform,
	}, nil
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	stats, err := getSystemStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	r := mux.NewRouter()

	// Serve static files
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// API endpoints
	r.HandleFunc("/api/stats", statsHandler).Methods("GET")

	// Serve the main page
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})

	port := ":8085"
	fmt.Printf("Server starting on http://localhost%s\n", port)
	log.Fatal(http.ListenAndServe(port, r))
}
