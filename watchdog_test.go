package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- samePIDs Tests ---

func TestSamePIDs_Identical(t *testing.T) {
	a := []int32{1, 2, 3}
	b := []int32{1, 2, 3}
	if !samePIDs(a, b) {
		t.Error("expected same PIDs to return true")
	}
}

func TestSamePIDs_DifferentOrder(t *testing.T) {
	a := []int32{3, 1, 2}
	b := []int32{1, 2, 3}
	if !samePIDs(a, b) {
		t.Error("expected same PIDs in different order to return true")
	}
}

func TestSamePIDs_DifferentValues(t *testing.T) {
	a := []int32{1, 2, 3}
	b := []int32{1, 2, 4}
	if samePIDs(a, b) {
		t.Error("expected different PIDs to return false")
	}
}

func TestSamePIDs_DifferentLengths(t *testing.T) {
	a := []int32{1, 2}
	b := []int32{1, 2, 3}
	if samePIDs(a, b) {
		t.Error("expected different lengths to return false")
	}
}

func TestSamePIDs_BothEmpty(t *testing.T) {
	if !samePIDs([]int32{}, []int32{}) {
		t.Error("expected two empty slices to be same")
	}
}

func TestSamePIDs_BothNil(t *testing.T) {
	if !samePIDs(nil, nil) {
		t.Error("expected two nil slices to be same")
	}
}

func TestSamePIDs_OneNilOneEmpty(t *testing.T) {
	if !samePIDs(nil, []int32{}) {
		t.Error("expected nil and empty slice to be same")
	}
}

func TestSamePIDs_SingleElement(t *testing.T) {
	if !samePIDs([]int32{42}, []int32{42}) {
		t.Error("expected same single element to return true")
	}
	if samePIDs([]int32{42}, []int32{99}) {
		t.Error("expected different single element to return false")
	}
}

// --- WatchdogEntry struct Tests ---

func TestWatchdogEntry_JSONSerialization(t *testing.T) {
	entry := WatchdogEntry{
		Name:         "nginx",
		Status:       "running",
		RestartCount: 0,
		LastSeenAt:   "2026-02-18T12:00:00Z",
		PIDs:         []int32{1234, 5678},
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded WatchdogEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Name != "nginx" {
		t.Errorf("expected Name 'nginx', got %q", decoded.Name)
	}
	if decoded.Status != "running" {
		t.Errorf("expected Status 'running', got %q", decoded.Status)
	}
	if len(decoded.PIDs) != 2 {
		t.Errorf("expected 2 PIDs, got %d", len(decoded.PIDs))
	}
}

func TestWatchdogPayload_JSONStructure(t *testing.T) {
	payload := WatchdogPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Entries: []WatchdogEntry{
			{Name: "nginx", Status: "running", RestartCount: 0, PIDs: []int32{100}},
			{Name: "redis", Status: "crashed", RestartCount: 0, PIDs: nil},
			{Name: "postgres", Status: "restarted", RestartCount: 1, PIDs: []int32{200}},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded WatchdogPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(decoded.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(decoded.Entries))
	}

	statuses := map[string]bool{}
	for _, e := range decoded.Entries {
		statuses[e.Status] = true
	}
	for _, expected := range []string{"running", "crashed", "restarted"} {
		if !statuses[expected] {
			t.Errorf("missing status %q in payload", expected)
		}
	}
}

// --- sendWatchdog HTTP Tests ---

func TestSendWatchdog_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/ingest/server-watchdog" {
			t.Errorf("expected /api/ingest/server-watchdog, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json")
		}
		if r.Header.Get("X-Agent-Token") != "test-token" {
			t.Errorf("expected X-Agent-Token test-token, got %q", r.Header.Get("X-Agent-Token"))
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "test-token"}
	payload := WatchdogPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Entries:   []WatchdogEntry{{Name: "test", Status: "running"}},
	}

	err := sendWatchdog(server.Client(), cfg, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendWatchdog_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "tok"}
	payload := WatchdogPayload{Timestamp: "now", Entries: nil}

	err := sendWatchdog(server.Client(), cfg, payload)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestSendWatchdog_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer server.Close()

	cfg := Config{BaseURL: server.URL, Token: "bad-token"}
	payload := WatchdogPayload{Timestamp: "now", Entries: nil}

	err := sendWatchdog(server.Client(), cfg, payload)
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}
