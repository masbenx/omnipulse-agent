package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- resolveServiceName Tests ---

func TestResolveServiceName_WellKnownPort(t *testing.T) {
	tests := []struct {
		port   int
		expect string
	}{
		{22, "SSH"},
		{80, "HTTP"},
		{443, "HTTPS"},
		{3306, "MySQL"},
		{5432, "PostgreSQL"},
		{6379, "Redis"},
		{27017, "MongoDB"},
		{9200, "Elasticsearch"},
		{8080, "HTTP Alt"},
	}

	for _, tt := range tests {
		result := resolveServiceName(tt.port, "")
		if result != tt.expect {
			t.Errorf("resolveServiceName(%d, \"\") = %q, expected %q", tt.port, result, tt.expect)
		}
	}
}

func TestResolveServiceName_ProcessNameOverride(t *testing.T) {
	tests := []struct {
		process string
		expect  string
	}{
		{"postgres", "PostgreSQL"},
		{"nginx", "Nginx"},
		{"redis-server", "Redis"},
		{"mysqld", "MySQL"},
		{"node", "Node.js"},
		{"php-fpm", "PHP-FPM"},
		{"caddy", "Caddy"},
		{"dockerd", "Docker"},
	}

	for _, tt := range tests {
		// Process name override should take priority over port
		result := resolveServiceName(9999, tt.process)
		if result != tt.expect {
			t.Errorf("resolveServiceName(9999, %q) = %q, expected %q", tt.process, result, tt.expect)
		}
	}
}

func TestResolveServiceName_ProcessOverridePriority(t *testing.T) {
	// When process name is "nginx" and port is 80, process override "Nginx" takes priority
	result := resolveServiceName(80, "nginx")
	if result != "Nginx" {
		t.Errorf("expected 'Nginx' (process override), got %q", result)
	}
}

func TestResolveServiceName_UnknownProcessUnknownPort(t *testing.T) {
	result := resolveServiceName(54321, "")
	if result != "Port 54321" {
		t.Errorf("expected 'Port 54321', got %q", result)
	}
}

func TestResolveServiceName_UnknownPortWithProcessName(t *testing.T) {
	result := resolveServiceName(54321, "myapp")
	if result != "myapp" {
		t.Errorf("expected 'myapp' as service name, got %q", result)
	}
}

func TestResolveServiceName_CaseInsensitive(t *testing.T) {
	// Process name lookup is lowercased
	result := resolveServiceName(9999, "Nginx")
	if result != "Nginx" { // lowercase "nginx" matches override
		t.Errorf("expected 'Nginx', got %q", result)
	}
}

// --- wellKnownPorts Map Tests ---

func TestWellKnownPorts_Coverage(t *testing.T) {
	expectedPorts := []int{21, 22, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995,
		1433, 1521, 2049, 3000, 3306, 3389, 5432, 5672, 5900, 6379, 6443,
		8080, 8443, 8888, 9090, 9200, 9300, 11211, 15672, 27017}

	for _, port := range expectedPorts {
		if _, ok := wellKnownPorts[port]; !ok {
			t.Errorf("port %d missing from wellKnownPorts map", port)
		}
	}
}

func TestWellKnownPorts_NoZeroPort(t *testing.T) {
	if _, ok := wellKnownPorts[0]; ok {
		t.Error("port 0 should not be in wellKnownPorts")
	}
}

// --- processNameOverrides Map Tests ---

func TestProcessNameOverrides_Coverage(t *testing.T) {
	expectedNames := []string{
		"postgres", "mysqld", "mariadbd", "redis-server", "mongod",
		"nginx", "apache2", "httpd", "caddy", "haproxy", "sshd",
		"dockerd", "containerd", "kubelet", "etcd", "java", "node",
		"python3", "python", "php-fpm", "dotnet", "rabbitmq-server",
		"memcached", "prometheus", "grafana-server", "minio",
	}

	for _, name := range expectedNames {
		if _, ok := processNameOverrides[name]; !ok {
			t.Errorf("process %q missing from processNameOverrides map", name)
		}
	}
}

// --- DiscoveredService struct Tests ---

func TestDiscoveredService_JSONSerialization(t *testing.T) {
	svc := DiscoveredService{
		Port:     5432,
		Protocol: "tcp",
		Process:  "postgres",
		Service:  "PostgreSQL",
		BindAddr: "0.0.0.0",
	}

	data, err := json.Marshal(svc)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded DiscoveredService
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Port != 5432 {
		t.Errorf("expected Port 5432, got %d", decoded.Port)
	}
	if decoded.Service != "PostgreSQL" {
		t.Errorf("expected Service 'PostgreSQL', got %q", decoded.Service)
	}
	if decoded.Protocol != "tcp" {
		t.Errorf("expected Protocol 'tcp', got %q", decoded.Protocol)
	}
}

func TestServiceDiscoveryPayload_JSONStructure(t *testing.T) {
	payload := ServiceDiscoveryPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Services: []DiscoveredService{
			{Port: 22, Protocol: "tcp", Process: "sshd", Service: "SSH", BindAddr: "0.0.0.0"},
			{Port: 5432, Protocol: "tcp", Process: "postgres", Service: "PostgreSQL", BindAddr: "127.0.0.1"},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded ServiceDiscoveryPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(decoded.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(decoded.Services))
	}
}

// --- sendServices HTTP Tests ---

func TestSendServices_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/ingest/server-services" {
			t.Errorf("expected /api/ingest/server-services, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json")
		}
		if r.Header.Get("X-Agent-Token") != "test-token" {
			t.Errorf("expected X-Agent-Token test-token")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Token:   "test-token",
		Timeout: 5 * time.Second,
	}
	services := []DiscoveredService{
		{Port: 22, Protocol: "tcp", Process: "sshd", Service: "SSH"},
	}

	err := sendServices(server.Client(), cfg, services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendServices_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "tok", Timeout: 5 * time.Second}
	err := sendServices(server.Client(), cfg, nil)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestSendServices_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "bad", Timeout: 5 * time.Second}
	err := sendServices(server.Client(), cfg, nil)
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}
