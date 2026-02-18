package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- ProcessInfo struct Tests ---

func TestProcessInfo_JSONSerialization(t *testing.T) {
	p := ProcessInfo{
		PID:    1234,
		Name:   "nginx",
		CPU:    12.5,
		Mem:    45.2,
		RSS:    1048576,
		User:   "www-data",
		Status: "running",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded ProcessInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.PID != 1234 {
		t.Errorf("expected PID 1234, got %d", decoded.PID)
	}
	if decoded.Name != "nginx" {
		t.Errorf("expected Name 'nginx', got %q", decoded.Name)
	}
	if decoded.CPU != 12.5 {
		t.Errorf("expected CPU 12.5, got %f", decoded.CPU)
	}
	if decoded.RSS != 1048576 {
		t.Errorf("expected RSS 1048576, got %d", decoded.RSS)
	}
}

func TestProcessesPayload_JSONStructure(t *testing.T) {
	payload := ProcessesPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Processes: []ProcessInfo{
			{PID: 1, Name: "init", CPU: 0.1, Mem: 0.5, RSS: 4096, User: "root", Status: "running"},
			{PID: 100, Name: "nginx", CPU: 5.0, Mem: 10.0, RSS: 8192, User: "www", Status: "sleeping"},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded ProcessesPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(decoded.Processes) != 2 {
		t.Fatalf("expected 2 processes, got %d", len(decoded.Processes))
	}
	if decoded.Processes[0].Name != "init" {
		t.Errorf("expected first process 'init', got %q", decoded.Processes[0].Name)
	}
}

func TestProcessesPayload_EmptyList(t *testing.T) {
	payload := ProcessesPayload{
		Timestamp: "2026-02-18T12:00:00Z",
		Processes: []ProcessInfo{},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded ProcessesPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(decoded.Processes) != 0 {
		t.Errorf("expected 0 processes, got %d", len(decoded.Processes))
	}
}

// --- sendProcesses HTTP Tests ---

func TestSendProcesses_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/ingest/server-processes" {
			t.Errorf("expected /api/ingest/server-processes, got %s", r.URL.Path)
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

	cfg := Config{BaseURL: server.URL, Token: "test-token"}
	payload := ProcessesPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Processes: []ProcessInfo{
			{PID: 1, Name: "test", CPU: 1.0, Mem: 2.0},
		},
	}

	err := sendProcesses(server.Client(), cfg, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendProcesses_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "tok"}
	payload := ProcessesPayload{Timestamp: "now"}

	err := sendProcesses(server.Client(), cfg, payload)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestSendProcesses_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "bad"}
	payload := ProcessesPayload{Timestamp: "now"}

	err := sendProcesses(server.Client(), cfg, payload)
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}

func TestSendProcesses_BadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "tok"}
	payload := ProcessesPayload{Timestamp: "now"}

	err := sendProcesses(server.Client(), cfg, payload)
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
}
