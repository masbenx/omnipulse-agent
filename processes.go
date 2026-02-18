package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// ProcessInfo represents a single process entry
type ProcessInfo struct {
	PID    int32   `json:"pid"`
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	Mem    float32 `json:"mem"`
	RSS    uint64  `json:"rss"`
	User   string  `json:"user"`
	Status string  `json:"status"`
}

// ProcessesPayload is the ingest payload
type ProcessesPayload struct {
	Timestamp string        `json:"timestamp"`
	Processes []ProcessInfo `json:"processes"`
}

// collectProcesses gathers top processes by CPU/memory usage
func collectProcesses() ([]ProcessInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}

	var result []ProcessInfo
	for _, p := range procs {
		name, _ := p.NameWithContext(ctx)
		if name == "" {
			continue
		}

		cpuPct, _ := p.CPUPercentWithContext(ctx)
		memPct, _ := p.MemoryPercentWithContext(ctx)

		var rss uint64
		if mem, err := p.MemoryInfoWithContext(ctx); err == nil && mem != nil {
			rss = mem.RSS
		}

		user, _ := p.UsernameWithContext(ctx)
		statusSlice, _ := p.StatusWithContext(ctx)
		status := "unknown"
		if len(statusSlice) > 0 {
			status = statusSlice[0]
		}

		result = append(result, ProcessInfo{
			PID:    p.Pid,
			Name:   name,
			CPU:    cpuPct,
			Mem:    memPct,
			RSS:    rss,
			User:   user,
			Status: status,
		})
	}

	// Sort by CPU desc, keep top 50
	sort.Slice(result, func(i, j int) bool {
		return result[i].CPU > result[j].CPU
	})
	if len(result) > 50 {
		result = result[:50]
	}

	return result, nil
}

// sendProcessesToBackend collects and sends process snapshot
func sendProcessesToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	procs, err := collectProcesses()
	if err != nil {
		logger.Printf("process collect error: %v", err)
		return
	}

	payload := ProcessesPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Processes: procs,
	}

	if err := sendProcesses(client, cfg, payload); err != nil {
		logger.Printf("processes ingest failed: %v", err)
	} else {
		logger.Printf("processes sent: %d entries", len(procs))
	}
}

// sendProcesses sends process payload to backend
func sendProcesses(client *http.Client, cfg Config, payload ProcessesPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := cfg.BaseURL + "/api/ingest/server-processes"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
