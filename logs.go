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

// collectLogs gathers recent system logs from journalctl or syslog fallback.
// sinceDuration controls how far back to look (e.g. 1*time.Minute for frequent polling).
func collectLogs(since time.Duration) ([]LogEntry, error) {
	entries, err := collectJournalctlLogs(since)
	if err == nil && len(entries) > 0 {
		return entries, nil
	}

	// Fallback: read /var/log/syslog or /var/log/messages
	return collectSyslogFallback(since)
}

// collectJournalctlLogs reads recent logs from journalctl in JSON format.
// since controls how far back to look for new entries.
func collectJournalctlLogs(since time.Duration) ([]LogEntry, error) {
	// Convert duration to journalctl's --since format (e.g. "1 minutes ago")
	sinceStr := fmt.Sprintf("%d seconds ago", int(since.Seconds()))
	cmd := exec.Command("journalctl",
		"--since", sinceStr,
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

// collectSyslogFallback reads the last lines from /var/log/syslog or /var/log/messages.
// since controls how many lines to tail (shorter durations = fewer lines).
func collectSyslogFallback(since time.Duration) ([]LogEntry, error) {
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

	// Scale tail lines by duration: ~10 lines per 30s, up to 100 for 5min
	tailLines := int(since.Minutes()) * 20
	if tailLines < 10 {
		tailLines = 10
	}
	if tailLines > 200 {
		tailLines = 200
	}
	cmd := exec.Command("tail", "-n", strconv.Itoa(tailLines), target)
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

// sendLogsToBackend collects and sends system logs.
// since controls how far back to look for new log entries.
func sendLogsToBackend(client *http.Client, cfg Config, logger *log.Logger, since time.Duration) {
	entries, err := collectLogs(since)
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

// collectFileLogs tails the last lines of a specific log file.
// since controls how many lines to tail (scaled by duration).
func collectFileLogs(path string, since time.Duration) []LogEntry {
	// Check if file exists and is readable
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil
	}

	// Scale tail lines: ~20 lines per minute of lookback
	tailLines := int(since.Minutes()) * 20
	if tailLines < 10 {
		tailLines = 10
	}
	if tailLines > 100 {
		tailLines = 100
	}

	cmd := exec.Command("tail", "-n", strconv.Itoa(tailLines), path)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	hostname, _ := os.Hostname()
	now := time.Now().UTC()

	// Use filename without extension as service name
	baseName := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		baseName = path[idx+1:]
	}
	// Strip .log extension
	baseName = strings.TrimSuffix(baseName, ".log")

	var entries []LogEntry
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Detect basic log level from content
		level := detectLogLevel(line)

		entries = append(entries, LogEntry{
			Timestamp: now.Format(time.RFC3339Nano),
			Level:     level,
			Service:   baseName,
			Host:      hostname,
			Message:   line,
		})
	}

	return entries
}

// detectLogLevel does a simple keyword-based detection of log level
func detectLogLevel(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warning"
	case strings.Contains(lower, "debug"):
		return "debug"
	default:
		return "info"
	}
}

// sendFileLogsToBackend collects and sends log entries from monitored .log files
func sendFileLogsToBackend(client *http.Client, cfg Config, logger *log.Logger, paths []string, since time.Duration) {
	var allEntries []LogEntry

	for _, p := range paths {
		entries := collectFileLogs(p, since)
		allEntries = append(allEntries, entries...)
	}

	if len(allEntries) == 0 {
		return
	}

	// Cap total entries
	if len(allEntries) > maxLogEntries {
		allEntries = allEntries[len(allEntries)-maxLogEntries:]
	}

	payload := LogIngestPayload{Entries: allEntries}
	if err := sendLogs(client, cfg, payload); err != nil {
		logger.Printf("file log ingest failed: %v", err)
	} else {
		logger.Printf("file logs sent: %d entries from %d files", len(allEntries), len(paths))
	}
}
