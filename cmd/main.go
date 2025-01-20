package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

type SystemStats struct {
	Hostname    string      `json:"hostname"`
	CPUUsage    []float64   `json:"cpu_usage"`
	MemoryStats *mem.VirtualMemoryStat `json:"memory_stats"`
	DiskStats   []disk.UsageStat       `json:"disk_stats"`
	Uptime      time.Duration          `json:"uptime"`
	Platform    string                 `json:"platform"`
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

	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}

	var diskStats []disk.UsageStat
	for _, partition := range partitions {
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			continue
		}
		diskStats = append(diskStats, *usage)
	}

	hostInfo, err := host.Info()
	if err != nil {
		return nil, err
	}

	return &SystemStats{
		Hostname:    hostname,
		CPUUsage:    cpuPercent,
		MemoryStats: memStats,
		DiskStats:   diskStats,
		Uptime:      time.Duration(hostInfo.Uptime) * time.Second,
		Platform:    hostInfo.Platform,
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

	port := ":8080"
	fmt.Printf("Server starting on http://localhost%s\n", port)
	log.Fatal(http.ListenAndServe(port, r))
}