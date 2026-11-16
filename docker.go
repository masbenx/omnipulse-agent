package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type DockerContainerItem struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Image      string  `json:"image"`
	State      string  `json:"state"`
	Status     string  `json:"status"`
	CPUPercent float64 `json:"cpu_percent"`
	MemBytes   int64   `json:"mem_bytes"`
	MemLimit   int64   `json:"mem_limit"`
	Ports      string  `json:"ports"`
}

type ServerDockerIngestReq struct {
	Timestamp  string                `json:"timestamp"`
	Containers []DockerContainerItem `json:"containers"`
}

const defaultDockerSocket = "/var/run/docker.sock"

func dockerSocketPath() string {
	if p := strings.TrimSpace(os.Getenv("OMNIPULSE_DOCKER_SOCKET")); p != "" {
		return p
	}
	return defaultDockerSocket
}

func newDockerHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

func sendDockerToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	sock := dockerSocketPath()
	if _, err := os.Stat(sock); err != nil {
		return
	}

	dClient := newDockerHTTPClient(sock)
	containers, err := collectDockerContainers(dClient)
	if err != nil {
		logger.Printf("docker collect error: %v", err)
		return
	}

	payload := ServerDockerIngestReq{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Containers: containers,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/api/ingest/server-docker", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		logger.Printf("docker ingest failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

func collectDockerContainers(dClient *http.Client) ([]DockerContainerItem, error) {
	resp, err := dClient.Get("http://docker/containers/json?all=1")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("docker API status=%d", resp.StatusCode)
	}

	var rawContainers []struct {
		ID     string   `json:"Id"`
		Names  []string `json:"Names"`
		Image  string   `json:"Image"`
		State  string   `json:"State"`
		Status string   `json:"Status"`
		Ports  []struct {
			PublicPort  int    `json:"PublicPort"`
			PrivatePort int    `json:"PrivatePort"`
			Type        string `json:"Type"`
		} `json:"Ports"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rawContainers); err != nil {
		return nil, err
	}

	out := make([]DockerContainerItem, 0, len(rawContainers))
	for _, rc := range rawContainers {
		name := rc.ID
		if len(rc.Names) > 0 {
			name = strings.TrimPrefix(rc.Names[0], "/")
		}
		shortID := rc.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		var portList []string
		for _, p := range rc.Ports {
			if p.PublicPort > 0 {
				portList = append(portList, fmt.Sprintf("%d->%d/%s", p.PublicPort, p.PrivatePort, p.Type))
			} else if p.PrivatePort > 0 {
				portList = append(portList, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
			}
		}

		item := DockerContainerItem{
			ID:     shortID,
			Name:   name,
			Image:  rc.Image,
			State:  rc.State,
			Status: rc.Status,
			Ports:  strings.Join(portList, ", "),
		}

		if strings.EqualFold(rc.State, "running") {
			cpuPct, memUsage, memLimit := fetchContainerStats(dClient, rc.ID)
			item.CPUPercent = cpuPct
			item.MemBytes = memUsage
			item.MemLimit = memLimit
		}
		out = append(out, item)
	}
	return out, nil
}

func fetchContainerStats(dClient *http.Client, containerID string) (float64, int64, int64) {
	resp, err := dClient.Get(fmt.Sprintf("http://docker/containers/%s/stats?stream=false", containerID))
	if err != nil {
		return 0, 0, 0
	}
	defer resp.Body.Close()

	var stats struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs     int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, 0, 0
	}

	var cpuPercent float64
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage)
	cpus := stats.CPUStats.OnlineCPUs
	if cpus <= 0 {
		cpus = 1
	}
	if sysDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / sysDelta) * float64(cpus) * 100.0
	}

	return cpuPercent, int64(stats.MemoryStats.Usage), int64(stats.MemoryStats.Limit)
}

func sendDockerLogsToBackend(client *http.Client, cfg Config, logger *log.Logger, since time.Duration) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OMNIPULSE_DOCKER_LOGS")), "false") {
		return
	}
	sock := dockerSocketPath()
	if _, err := os.Stat(sock); err != nil {
		return
	}

	dClient := newDockerHTTPClient(sock)
	resp, err := dClient.Get("http://docker/containers/json")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var running []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&running); err != nil {
		return
	}

	hostname, _ := os.Hostname()
	sinceUnix := time.Now().Add(-since).Unix()
	var entries []LogEntry

	for _, c := range running {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		// Avoid self-referential log storms from omnipulse containers
		if strings.HasPrefix(name, "omnipulse-") {
			continue
		}

		logURL := fmt.Sprintf("http://docker/containers/%s/logs?stdout=1&stderr=1&since=%d&timestamps=1&tail=50", c.ID, sinceUnix)
		lResp, err := dClient.Get(logURL)
		if err != nil {
			continue
		}
		rawBytes, _ := io.ReadAll(io.LimitReader(lResp.Body, 256*1024))
		lResp.Body.Close()

		lines := parseDockerLogPayload(rawBytes)
		for _, line := range lines {
			ts, msg := splitDockerTimestamp(line)
			if strings.TrimSpace(msg) == "" {
				continue
			}
			level := inferLogLevel(msg)
			entries = append(entries, LogEntry{
				Timestamp: ts,
				Level:     level,
				Service:   name,
				Host:      hostname,
				Message:   msg,
			})
			if len(entries) >= maxLogEntries {
				break
			}
		}
		if len(entries) >= maxLogEntries {
			break
		}
	}

	if len(entries) > 0 {
		if err := sendLogs(client, cfg, LogIngestPayload{Entries: entries}); err != nil {
			logger.Printf("docker logs ingest failed: %v", err)
		}
	}
}

// parseDockerLogPayload decodes either Docker multiplexed 8-byte framed streams or plain TTY lines.
func parseDockerLogPayload(data []byte) []string {
	var lines []string
	idx := 0
	// Heuristic: Docker multiplexed stream starts with stream type 1 (stdout) or 2 (stderr) and bytes 1..3 are 0x00.
	if len(data) >= 8 && (data[0] == 1 || data[0] == 2) && data[1] == 0 && data[2] == 0 && data[3] == 0 {
		for idx+8 <= len(data) {
			frameLen := int(binary.BigEndian.Uint32(data[idx+4 : idx+8]))
			idx += 8
			if frameLen <= 0 || idx+frameLen > len(data) {
				break
			}
			chunk := string(data[idx : idx+frameLen])
			for _, l := range strings.Split(strings.TrimRight(chunk, "\r\n"), "\n") {
				if l != "" {
					lines = append(lines, l)
				}
			}
			idx += frameLen
		}
		return lines
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		l := scanner.Text()
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func splitDockerTimestamp(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 {
		if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
			return t.UTC().Format(time.RFC3339Nano), parts[1]
		}
	}
	return time.Now().UTC().Format(time.RFC3339Nano), line
}

func inferLogLevel(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic") || strings.Contains(lower, " 500 ") || strings.Contains(lower, " 502 "):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	default:
		return "info"
	}
}
