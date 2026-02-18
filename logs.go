package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// LogEntry represents a single log line to send to the backend
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Service   string `json:"service"`
	Host      string `json:"host"`
	Message   string `json:"message"`
}

// LogIngestPayload is the payload sent to /api/ingest/server-logs
type LogIngestPayload struct {
	Entries []LogEntry `json:"entries"`
}

// journalctlEntry represents a parsed journalctl JSON line
type journalctlEntry struct {
	Message           string `json:"MESSAGE"`
	Priority          string `json:"PRIORITY"`
	SyslogIdentifier  string `json:"SYSLOG_IDENTIFIER"`
	Comm              string `json:"_COMM"`
	Hostname          string `json:"_HOSTNAME"`
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"`
}

const maxLogEntries = 200

// collectLogs gathers recent system logs from journalctl or syslog fallback
func collectLogs() ([]LogEntry, error) {
	entries, err := collectJournalctlLogs()
	if err == nil && len(entries) > 0 {
		return entries, nil
	}

	// Fallback: read /var/log/syslog or /var/log/messages
	return collectSyslogFallback()
}

// collectJournalctlLogs reads recent logs from journalctl in JSON format
func collectJournalctlLogs() ([]LogEntry, error) {
	cmd := exec.Command("journalctl",
		"--since", "5 minutes ago",
		"--output", "json",
		"--no-pager",
		"-n", strconv.Itoa(maxLogEntries),
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}

	hostname, _ := os.Hostname()
	var entries []LogEntry

	scanner := bufio.NewScanner(bytes.NewReader(out))
	// Increase buffer for potentially long log lines
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var je journalctlEntry
		if err := json.Unmarshal(line, &je); err != nil {
			continue
		}

		if je.Message == "" {
			continue
		}

		// Skip omnipulse-agent's own logs to avoid feedback loop
		svc := je.SyslogIdentifier
		if svc == "" {
			svc = je.Comm
		}
		if svc == "omnipulse-agent" || svc == serviceName {
			continue
		}

		ts := parseJournalTimestamp(je.RealtimeTimestamp)
		level := mapJournalPriority(je.Priority)

		host := je.Hostname
		if host == "" {
			host = hostname
		}

		entries = append(entries, LogEntry{
			Timestamp: ts,
			Level:     level,
			Service:   svc,
			Host:      host,
			Message:   je.Message,
		})
	}

	if len(entries) > maxLogEntries {
		entries = entries[len(entries)-maxLogEntries:]
	}

	return entries, nil
}

// collectSyslogFallback reads the last lines from /var/log/syslog or /var/log/messages
func collectSyslogFallback() ([]LogEntry, error) {
	logFiles := []string{"/var/log/syslog", "/var/log/messages"}
	var target string
	for _, f := range logFiles {
		if _, err := os.Stat(f); err == nil {
			target = f
			break
		}
	}
	if target == "" {
		return nil, fmt.Errorf("no syslog file found")
	}

	cmd := exec.Command("tail", "-n", "50", target)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tail %s: %w", target, err)
	}

	hostname, _ := os.Hostname()
	now := time.Now().UTC()
	var entries []LogEntry

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Skip own agent logs
		if strings.Contains(line, "omnipulse-agent") {
			continue
		}

		// Basic syslog parsing: "Mon DD HH:MM:SS hostname service[pid]: message"
		svc, msg := parseSyslogLine(line)

		entries = append(entries, LogEntry{
			Timestamp: now.Format(time.RFC3339),
			Level:     "info",
			Service:   svc,
			Host:      hostname,
			Message:   msg,
		})
	}

	return entries, nil
}

// parseJournalTimestamp converts journalctl's __REALTIME_TIMESTAMP (microseconds since epoch) to RFC3339
func parseJournalTimestamp(usecStr string) string {
	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(usec/1_000_000, (usec%1_000_000)*1000).UTC().Format(time.RFC3339Nano)
}

// mapJournalPriority maps journalctl PRIORITY (0-7) to log level string
func mapJournalPriority(p string) string {
	switch p {
	case "0", "1", "2", "3": // emerg, alert, crit, err
		return "error"
	case "4": // warning
		return "warning"
	case "5", "6": // notice, info
		return "info"
	case "7": // debug
		return "debug"
	default:
		return "info"
	}
}

// parseSyslogLine does basic parsing of a syslog line to extract service and message
func parseSyslogLine(line string) (service, message string) {
	// Format: "Mon DD HH:MM:SS hostname service[pid]: message"
	// Try to find service name after hostname
	parts := strings.SplitN(line, ": ", 2)
	if len(parts) == 2 {
		message = parts[1]
		// Parse prefix to find service name
		prefixParts := strings.Fields(parts[0])
		if len(prefixParts) >= 5 {
			svc := prefixParts[4]
			// Strip [pid] suffix
			if idx := strings.Index(svc, "["); idx > 0 {
				svc = svc[:idx]
			}
			service = svc
		}
	} else {
		message = line
	}
	if service == "" {
		service = "syslog"
	}
	return
}

// sendLogsToBackend collects and sends system logs
func sendLogsToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	entries, err := collectLogs()
	if err != nil {
		logger.Printf("log collect error: %v", err)
		return
	}

	if len(entries) == 0 {
		return
	}

	payload := LogIngestPayload{Entries: entries}
	if err := sendLogs(client, cfg, payload); err != nil {
		logger.Printf("log ingest failed: %v", err)
	} else {
		logger.Printf("logs sent: %d entries", len(entries))
	}
}

// sendLogs sends log payload to backend
func sendLogs(client *http.Client, cfg Config, payload LogIngestPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := cfg.BaseURL + "/api/ingest/server-logs"
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
