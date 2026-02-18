package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// WatchdogEntry tracks a watched process and its crash/restart status.
type WatchdogEntry struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"` // running, crashed, restarted
	RestartCount int     `json:"restart_count"`
	LastSeenAt   string  `json:"last_seen_at"`
	PIDs         []int32 `json:"pids"`
}

// WatchdogPayload is sent to the backend.
type WatchdogPayload struct {
	Timestamp string          `json:"timestamp"`
	Entries   []WatchdogEntry `json:"entries"`
}

// watchdogState maintains previous snapshot for diff detection.
type watchdogState struct {
	mu       sync.Mutex
	previous map[string]watchdogProcess // key: process name
}

type watchdogProcess struct {
	PIDs       []int32
	LastSeenAt time.Time
}

var wdState = &watchdogState{
	previous: make(map[string]watchdogProcess),
}

// collectWatchdog compares current running processes against the previous snapshot
// to detect crashes (process disappeared) and restarts (process reappeared with new PID).
func collectWatchdog() ([]WatchdogEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}

	// Build current process map: name -> []PIDs
	current := make(map[string][]int32)
	for _, p := range procs {
		name, _ := p.NameWithContext(ctx)
		if name == "" {
			continue
		}
		current[name] = append(current[name], p.Pid)
	}

	wdState.mu.Lock()
	defer wdState.mu.Unlock()

	now := time.Now().UTC()
	var entries []WatchdogEntry

	// Check previous processes against current
	for name, prev := range wdState.previous {
		pids, exists := current[name]
		if !exists {
			// Process crashed — was running before but gone now
			entries = append(entries, WatchdogEntry{
				Name:         name,
				Status:       "crashed",
				RestartCount: 0,
				LastSeenAt:   prev.LastSeenAt.Format(time.RFC3339),
				PIDs:         nil,
			})
		} else {
			// Check if PIDs changed (restart detection)
			status := "running"
			restartCount := 0
			if !samePIDs(prev.PIDs, pids) {
				status = "restarted"
				restartCount = 1
			}
			entries = append(entries, WatchdogEntry{
				Name:         name,
				Status:       status,
				RestartCount: restartCount,
				LastSeenAt:   now.Format(time.RFC3339),
				PIDs:         pids,
			})
		}
	}

	// New processes (not in previous snapshot)
	for name, pids := range current {
		if _, existed := wdState.previous[name]; !existed {
			entries = append(entries, WatchdogEntry{
				Name:         name,
				Status:       "running",
				RestartCount: 0,
				LastSeenAt:   now.Format(time.RFC3339),
				PIDs:         pids,
			})
		}
	}

	// Update state for next comparison
	wdState.previous = make(map[string]watchdogProcess, len(current))
	for name, pids := range current {
		wdState.previous[name] = watchdogProcess{
			PIDs:       pids,
			LastSeenAt: now,
		}
	}

	// Sort by name for consistency
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Keep only top 100 entries
	if len(entries) > 100 {
		entries = entries[:100]
	}

	return entries, nil
}

// samePIDs checks if two PID slices contain the same PIDs.
func samePIDs(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	setA := make(map[int32]bool, len(a))
	for _, pid := range a {
		setA[pid] = true
	}
	for _, pid := range b {
		if !setA[pid] {
			return false
		}
	}
	return true
}

func sendWatchdogToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	entries, err := collectWatchdog()
	if err != nil {
		logger.Printf("watchdog collect error: %v", err)
		return
	}

	// First run: just building baseline, skip sending
	if len(entries) == 0 {
		logger.Println("watchdog: baseline snapshot stored")
		return
	}

	// Count interesting events (crashed/restarted)
	crashed, restarted := 0, 0
	for _, e := range entries {
		switch e.Status {
		case "crashed":
			crashed++
		case "restarted":
			restarted++
		}
	}

	payload := WatchdogPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Entries:   entries,
	}

	if err := sendWatchdog(client, cfg, payload); err != nil {
		logger.Printf("watchdog ingest failed: %v", err)
	} else {
		logger.Printf("watchdog sent: %d entries (crashed=%d restarted=%d)",
			len(entries), crashed, restarted)
	}
}

func sendWatchdog(client *http.Client, cfg Config, payload WatchdogPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := cfg.BaseURL + "/api/ingest/server-watchdog"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", cfg.Token)
	req.Header.Set("User-Agent", "omnipulse-agent/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
