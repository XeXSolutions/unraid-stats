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
		State: "Unknown",
	}

	// Read array status from mdstat
	mdstatPath := "/host/var/run/mdstat"
	if _, err := os.Stat(mdstatPath); os.IsNotExist(err) {
		log.Printf("mdstat not found at %s, trying fallback path", mdstatPath)
		mdstatPath = "/var/run/mdstat" // Fallback for testing
	}

	mdstatData, err := ioutil.ReadFile(mdstatPath)
	if err != nil {
		log.Printf("Warning: Could not read mdstat: %v", err)
	} else {
		mdstatContent := string(mdstatData)
		if strings.Contains(mdstatContent, "active") {
			arrayStatus.State = "Started"
			arrayStatus.Protection = "Protected"
		} else if strings.Contains(mdstatContent, "inactive") {
			arrayStatus.State = "Stopped"
			arrayStatus.Protection = "Not Protected"
		}
	}

	// Read disks.ini for disk information
	var diskInfos []DiskInfo

	// Try different possible paths for disks.ini
	possiblePaths := []string{
		"/host/var/local/emhttp/disks.ini",
		"/var/local/emhttp/disks.ini",
		"/etc/unraid/disks.ini",
	}

	var disksIniData []byte
	var disksIniPath string
	var readErr error

	for _, path := range possiblePaths {
		log.Printf("Trying to read disks.ini from: %s", path)
		if _, err := os.Stat(path); err == nil {
			// File exists, try to read it
			disksIniData, readErr = ioutil.ReadFile(path)
			if readErr == nil {
				disksIniPath = path
				log.Printf("Successfully read disks.ini from: %s", path)
				break
			}
			log.Printf("Error reading %s: %v", path, readErr)
		} else {
			log.Printf("File not found at %s: %v", path, err)
		}
	}

	if disksIniPath == "" {
		log.Printf("Warning: Could not find disks.ini in any of the expected locations")
		// Try to list the contents of the directories to debug
		for _, dir := range []string{"/host/var/local/emhttp", "/var/local/emhttp", "/etc/unraid"} {
			if files, err := ioutil.ReadDir(dir); err == nil {
				log.Printf("Contents of %s:", dir)
				for _, f := range files {
					log.Printf("  %s (size: %d, mode: %s)", f.Name(), f.Size(), f.Mode())
				}
			} else {
				log.Printf("Could not read directory %s: %v", dir, err)
			}
		}
		return arrayStatus, diskInfos, fmt.Errorf("could not find disks.ini")
	}

	// Parse disks.ini content
	lines := strings.Split(string(disksIniData), "\n")
	var currentDisk string
	diskMap := make(map[string]map[string]string)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentDisk = strings.Trim(line, "[]")
			diskMap[currentDisk] = make(map[string]string)
		} else if currentDisk != "" && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				diskMap[currentDisk][key] = value
			}
		}
	}

	// Process disk information
	for diskName, diskData := range diskMap {
		if diskName == "parity" || diskName == "parity2" {
			continue // Skip parity disks for now as they're handled differently
		}

		diskType := "data"
		if strings.HasPrefix(diskName, "cache") {
			diskType = "cache"
		}

		devicePath := diskData["device"]
		if devicePath == "" {
			log.Printf("No device path found for disk: %s", diskName)
			continue
		}

		// Get disk usage information
		usage, err := disk.Usage(devicePath)
		if err != nil {
			log.Printf("Warning: Could not get disk usage for %s (%s): %v", diskName, devicePath, err)
			continue
		}

		diskInfo := DiskInfo{
			Path:        devicePath,
			Name:        diskName,
			Total:       usage.Total,
			Used:        usage.Used,
			Free:        usage.Free,
			UsedPercent: usage.UsedPercent,
			Type:        diskType,
		}

		if diskType == "data" {
			arrayStatus.TotalCapacity += usage.Total
			arrayStatus.UsedSpace += usage.Used
		} else if diskType == "cache" {
			arrayStatus.CacheSize += usage.Total
		}

		diskInfos = append(diskInfos, diskInfo)
	}

	// Add parity disks
	parityDevices := []string{"parity", "parity2"}
	for _, parityName := range parityDevices {
		if parityData, exists := diskMap[parityName]; exists {
			devicePath := parityData["device"]
			if devicePath == "" {
				log.Printf("No device path found for parity disk: %s", parityName)
				continue
			}

			usage, err := disk.Usage(devicePath)
			if err != nil {
				log.Printf("Warning: Could not get disk usage for parity disk %s (%s): %v", parityName, devicePath, err)
				continue
			}

			diskInfo := DiskInfo{
				Path:        devicePath,
				Name:        parityName,
				Total:       usage.Total,
				Used:        usage.Used,
				Free:        usage.Free,
				UsedPercent: usage.UsedPercent,
				Type:        "parity",
			}

			arrayStatus.ParitySize += usage.Total
			diskInfos = append(diskInfos, diskInfo)
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
