package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

// DiscoveredService represents a listening service found on the host
type DiscoveredService struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Process  string `json:"process"`
	Service  string `json:"service"`
	BindAddr string `json:"bind_addr"`
}

// ServiceDiscoveryPayload is sent to POST /api/ingest/server-services
type ServiceDiscoveryPayload struct {
	Timestamp string              `json:"timestamp"`
	Services  []DiscoveredService `json:"services"`
}

// wellKnownPorts maps common ports to human-readable service names
var wellKnownPorts = map[int]string{
	21:    "FTP",
	22:    "SSH",
	25:    "SMTP",
	53:    "DNS",
	80:    "HTTP",
	110:   "POP3",
	143:   "IMAP",
	443:   "HTTPS",
	465:   "SMTPS",
	587:   "SMTP Submission",
	993:   "IMAPS",
	995:   "POP3S",
	1433:  "MSSQL",
	1521:  "Oracle DB",
	2049:  "NFS",
	3000:  "Dev Server",
	3306:  "MySQL",
	3389:  "RDP",
	5432:  "PostgreSQL",
	5672:  "RabbitMQ",
	5900:  "VNC",
	6379:  "Redis",
	6443:  "Kubernetes API",
	8080:  "HTTP Alt",
	8443:  "HTTPS Alt",
	8888:  "HTTP Alt",
	9090:  "Prometheus",
	9200:  "Elasticsearch",
	9300:  "Elasticsearch Transport",
	11211: "Memcached",
	15672: "RabbitMQ Management",
	27017: "MongoDB",
}

// processNameOverrides maps process binary names to friendlier service labels
var processNameOverrides = map[string]string{
	"postgres":        "PostgreSQL",
	"mysqld":          "MySQL",
	"mariadbd":        "MariaDB",
	"redis-server":    "Redis",
	"mongod":          "MongoDB",
	"nginx":           "Nginx",
	"apache2":         "Apache",
	"httpd":           "Apache",
	"caddy":           "Caddy",
	"haproxy":         "HAProxy",
	"sshd":            "SSH",
	"dockerd":         "Docker",
	"containerd":      "Containerd",
	"kubelet":         "Kubelet",
	"etcd":            "etcd",
	"java":            "Java App",
	"node":            "Node.js",
	"python3":         "Python App",
	"python":          "Python App",
	"php-fpm":         "PHP-FPM",
	"dotnet":          ".NET App",
	"rabbitmq-server": "RabbitMQ",
	"memcached":       "Memcached",
	"prometheus":      "Prometheus",
	"grafana-server":  "Grafana",
	"minio":           "MinIO",
}

// collectServices scans for listening TCP/UDP services using gopsutil
func collectServices() ([]DiscoveredService, error) {
	conns, err := gnet.Connections("inet")
	if err != nil {
		return nil, fmt.Errorf("net.Connections: %w", err)
	}

	// Deduplicate by port — one entry per unique port
	seen := make(map[int]bool)
	var services []DiscoveredService

	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		port := int(c.Laddr.Port)
		if port <= 0 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true

		protocol := "tcp"
		if c.Type == 2 { // SOCK_DGRAM = UDP
			protocol = "udp"
		}

		bindAddr := c.Laddr.IP
		if bindAddr == "" {
			bindAddr = "0.0.0.0"
		}

		processName := resolveProcessName(c.Pid)
		serviceName := resolveServiceName(port, processName)

		services = append(services, DiscoveredService{
			Port:     port,
			Protocol: protocol,
			Process:  processName,
			Service:  serviceName,
			BindAddr: bindAddr,
		})
	}

	return services, nil
}

// resolveProcessName tries to get the process name for a given PID
func resolveProcessName(pid int32) string {
	if pid <= 0 {
		return ""
	}

	// Try /proc/<pid>/comm first (Linux fast path)
	commPath := "/proc/" + strconv.Itoa(int(pid)) + "/comm"
	if data, err := os.ReadFile(commPath); err == nil {
		name := strings.TrimSpace(string(data))
		if name != "" {
			return name
		}
	}

	// Fallback to gopsutil
	p, err := process.NewProcess(pid)
	if err != nil {
		return ""
	}
	name, err := p.Name()
	if err != nil {
		return ""
	}
	return name
}

// resolveServiceName determines a human-friendly label from port or process name
func resolveServiceName(port int, processName string) string {
	// 1. Check process name overrides (most accurate)
	if processName != "" {
		lower := strings.ToLower(processName)
		if label, ok := processNameOverrides[lower]; ok {
			return label
		}
	}

	// 2. Check well-known port map
	if label, ok := wellKnownPorts[port]; ok {
		return label
	}

	// 3. Use process name as-is if available
	if processName != "" {
		return processName
	}

	// 4. Unknown
	return fmt.Sprintf("Port %d", port)
}

// sendServices sends discovered services to the backend
func sendServices(client *http.Client, cfg Config, services []DiscoveredService) error {
	payload := ServiceDiscoveryPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Services:  services,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := cfg.BaseURL + "/api/ingest/server-services"
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, msg)
	}

	return nil
}
