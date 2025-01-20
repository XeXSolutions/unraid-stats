package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
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
	arrayStatus := &ArrayStatus{
		State:      "Stopped",
		Protection: "Not Protected",
	}

	var diskInfos []DiskInfo

	// Check array status from mdstat
	mdstatPath := "/var/run/mdstat"
	if mdstatData, err := ioutil.ReadFile(mdstatPath); err == nil {
		mdstatContent := string(mdstatData)
		log.Printf("Read mdstat content: %s", mdstatContent)
		if strings.Contains(mdstatContent, "md0") {
			arrayStatus.State = "Started"
			arrayStatus.Protection = "Protected"
			log.Printf("Array detected as Started and Protected")
		}
	} else {
		log.Printf("Could not read mdstat: %v", err)
		// Try alternate path
		mdstatPath = "/host/proc/mdstat"
		if mdstatData, err := ioutil.ReadFile(mdstatPath); err == nil {
			mdstatContent := string(mdstatData)
			log.Printf("Read mdstat content from alternate path: %s", mdstatContent)
			if strings.Contains(mdstatContent, "md0") {
				arrayStatus.State = "Started"
				arrayStatus.Protection = "Protected"
				log.Printf("Array detected as Started and Protected")
			}
		} else {
			log.Printf("Could not read mdstat from alternate path: %v", err)
		}
	}

	// Read array disks from /mnt directly
	basePath := "/host/mnt"
	log.Printf("Checking disk paths in %s...", basePath)

	// Read directory contents
	entries, err := os.ReadDir(basePath)
	if err != nil {
		log.Printf("Error reading directory %s: %v", basePath, err)
		return arrayStatus, diskInfos, nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		path := fmt.Sprintf("%s/%s", basePath, name)
		var diskType string

		switch {
		case strings.HasPrefix(name, "disk"):
			diskType = "data"
		case name == "cache":
			diskType = "cache"
		case name == "user":
			diskType = "user"
		default:
			continue
		}

		log.Printf("Found disk directory: %s (type: %s)", path, diskType)
		usage, err := disk.Usage(path)
		if err != nil {
			log.Printf("Warning: Could not get disk usage for %s: %v", path, err)
			continue
		}

		diskInfo := DiskInfo{
			Path:        path,
			Name:        name,
			Total:       usage.Total,
			Used:        usage.Used,
			Free:        usage.Free,
			UsedPercent: usage.UsedPercent,
			Type:        diskType,
		}

		if diskType == "data" {
			arrayStatus.TotalCapacity += usage.Total
			arrayStatus.UsedSpace += usage.Used
			log.Printf("Added data disk %s: total=%d, used=%d", name, usage.Total, usage.Used)
		} else if diskType == "cache" {
			arrayStatus.CacheSize += usage.Total
			log.Printf("Added cache disk %s: total=%d", name, usage.Total)
		}

		diskInfos = append(diskInfos, diskInfo)
	}

	log.Printf("Found %d disks total", len(diskInfos))
	log.Printf("Array status: state=%s, protection=%s, total=%d, used=%d",
		arrayStatus.State, arrayStatus.Protection, arrayStatus.TotalCapacity, arrayStatus.UsedSpace)

	return arrayStatus, diskInfos, nil
}

func getSystemStats() (*SystemStats, error) {
	// Try to get Unraid server name first
	hostname := "Unknown"
	platform := "Unraid" // Default to Unraid

	// Read var.ini for server name and version
	if varData, err := ioutil.ReadFile("/var/local/emhttp/var.ini"); err == nil {
		lines := strings.Split(string(varData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "NAME=") {
				hostname = strings.Trim(strings.TrimPrefix(line, "NAME="), "\"")
			} else if strings.HasPrefix(line, "version=") {
				version := strings.Trim(strings.TrimPrefix(line, "version="), "\"")
				if version != "" {
					platform = fmt.Sprintf("Unraid %s", version)
				}
			}
		}
	} else {
		// Fallback to system hostname
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			hostname = "Unknown"
		}
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
			// Skip loopback and docker interfaces
			if !strings.HasPrefix(counter.Name, "lo") &&
				!strings.HasPrefix(counter.Name, "docker") &&
				!strings.HasPrefix(counter.Name, "br-") &&
				!strings.HasPrefix(counter.Name, "veth") {
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
		Platform:     platform,
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
