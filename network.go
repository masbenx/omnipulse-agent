package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type NetworkIngestReq struct {
	Timestamp string              `json:"timestamp"`
	Flows     []NetworkFlowMetric `json:"flows"`
}

type NetworkFlowMetric struct {
	DestIP          string  `json:"dest_ip"`
	DestPort        int     `json:"dest_port"`
	SourceIP        string  `json:"source_ip"`
	SourcePort      int     `json:"source_port"`
	Protocol        string  `json:"protocol"`
	ProcessName     string  `json:"process_name"`
	BytesSent       int64   `json:"bytes_sent"`
	BytesReceived   int64   `json:"bytes_received"`
	PacketsSent     int64   `json:"packets_sent"`
	PacketsReceived int64   `json:"packets_received"`
	Retransmissions int     `json:"retransmissions"`
	RTTMs           float64 `json:"rtt_ms"`
}

var (
	// Regex to extract info from ss output
	// Example: ESTAB 0 0 127.0.0.1:443 127.0.0.1:56788 users:(("nginx",pid=123,fd=456))
	ssLineRegex = regexp.MustCompile(`^(\S+)\s+\d+\s+\d+\s+(\S+):(\d+)\s+(\S+):(\d+)(?:\s+users:\(\(([^)]+)\)\))?`)
	// Example: rtt:0.123/0.021 bytes_sent:1234 bytes_retrans:10 packets_sent:100 ...
	rttRegex      = regexp.MustCompile(`rtt:([\d\.]+)/`)
	bytesSentRegex = regexp.MustCompile(`bytes_sent:(\d+)`)
	retransRegex  = regexp.MustCompile(`bytes_retrans:(\d+)`)
	receivedRegex = regexp.MustCompile(`bytes_received:(\d+)`)
)

func sendNetworkFlowsToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	flows, err := collectNetworkFlows()
	if err != nil {
		// ss might not be available on all systems (e.g. non-linux)
		// Or permission denied if not root.
		// We don't want to spam error logs if it's expectedly unavailable.
		return
	}

	if len(flows) == 0 {
		return
	}

	payload := NetworkIngestReq{
		Timestamp: time.Now().Format(time.RFC3339),
		Flows:     flows,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		logger.Printf("network flows marshal error: %v", err)
		return
	}

	url := fmt.Sprintf("%s/api/ingest/network-flows", strings.TrimSuffix(cfg.BaseURL, "/"))
	req, err := http.NewRequest("POST", url, strings.NewReader(string(data)))
	if err != nil {
		logger.Printf("network flows request error: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", cfg.Token)
	req.Header.Set("User-Agent", "omnipulse-agent/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		logger.Printf("network flows ingest failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		logger.Printf("network flows ingest error: status=%d body=%s", resp.StatusCode, string(body))
	}
}

func collectNetworkFlows() ([]NetworkFlowMetric, error) {
	// ss -tnip:
	// -t: tcp
	// -n: numeric ports
	// -i: internal info (rtt, retrans)
	// -p: processes (requires root/sudo usually)
	cmd := exec.Command("ss", "-tnip")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	var flows []NetworkFlowMetric

	var currentFlow *NetworkFlowMetric

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") {
			continue
		}

		// Check if it's a primary connection line
		matches := ssLineRegex.FindStringSubmatch(line)
		if len(matches) > 0 {
			// If we were processing a previous flow, add it now
			if currentFlow != nil {
				flows = append(flows, *currentFlow)
			}

			state := matches[1]
			if state != "ESTAB" && state != "CLOSE-WAIT" {
				currentFlow = nil
				continue
			}

			srcIP := matches[2]
			srcPort, _ := strconv.Atoi(matches[3])
			destIP := matches[4]
			destPort, _ := strconv.Atoi(matches[5])
			processInfo := matches[6]

			procName := ""
			if processInfo != "" {
				// Format: "nginx",pid=123,fd=456
				parts := strings.Split(processInfo, ",")
				procName = strings.Trim(parts[0], "\"")
			}

			currentFlow = &NetworkFlowMetric{
				SourceIP:    srcIP,
				SourcePort:  srcPort,
				DestIP:      destIP,
				DestPort:    destPort,
				Protocol:    "tcp",
				ProcessName: procName,
			}
			continue
		}

		// Check if it's an info line for the current flow
		if currentFlow != nil {
			if rttMatch := rttRegex.FindStringSubmatch(line); len(rttMatch) > 1 {
				currentFlow.RTTMs, _ = strconv.ParseFloat(rttMatch[1], 64)
			}
			if sentMatch := bytesSentRegex.FindStringSubmatch(line); len(sentMatch) > 1 {
				currentFlow.BytesSent, _ = strconv.ParseInt(sentMatch[1], 10, 64)
			}
			if recvMatch := receivedRegex.FindStringSubmatch(line); len(recvMatch) > 1 {
				currentFlow.BytesReceived, _ = strconv.ParseInt(recvMatch[1], 10, 64)
			}
			if retransMatch := retransRegex.FindStringSubmatch(line); len(retransMatch) > 1 {
				retransBytes, _ := strconv.ParseInt(retransMatch[1], 10, 64)
				// Best guess for retransmissions count if we only have bytes
				// but ss usually provides data in bits/bytes.
				// We'll just store the value we find.
				currentFlow.Retransmissions = int(retransBytes) 
			}
		}
	}

	// Add final flow
	if currentFlow != nil {
		flows = append(flows, *currentFlow)
	}

	return flows, nil
}
