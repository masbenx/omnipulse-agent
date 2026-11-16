package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDockerLogPayloadMultiplexed(t *testing.T) {
	msg1 := "2026-11-16T10:00:00Z hello stdout\n"
	msg2 := "2026-11-16T10:00:01Z error stderr\n"

	var buf []byte
	for _, pair := range []struct {
		stream byte
		text   string
	}{{1, msg1}, {2, msg2}} {
		hdr := make([]byte, 8)
		hdr[0] = pair.stream
		binary.BigEndian.PutUint32(hdr[4:8], uint32(len(pair.text)))
		buf = append(buf, hdr...)
		buf = append(buf, []byte(pair.text)...)
	}

	lines := parseDockerLogPayload(buf)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	ts, body := splitDockerTimestamp(lines[1])
	if ts != "2026-11-16T10:00:01Z" || body != "error stderr" {
		t.Fatalf("unexpected split: ts=%q body=%q", ts, body)
	}
	if inferLogLevel(body) != "error" {
		t.Fatalf("expected error level")
	}
}

func TestCollectSeaweedFSMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dir/status":
			_, _ = w.Write([]byte(`{"Topology":{"Max":100,"Free":88,"DataCenters":[{"Id":"dc1","Racks":[{"DataNodes":[{"Url":"","Volumes":12,"Max":100,"FileCount":450}]}]}]}}`))
		case "/cluster/status":
			_, _ = w.Write([]byte(`{"IsLeader":true,"Leader":"127.0.0.1:9334","Peers":[]}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	st := StorageConfigItem{
		ID:            7,
		Name:          "SeaweedFS",
		Provider:      "seaweedfs",
		EndpointURL:   srv.URL,
		S3EndpointURL: srv.URL,
	}

	m := collectSeaweedFSMetrics(srv.Client(), st)
	if !m.IsHealthy || !m.S3Healthy {
		t.Fatalf("expected healthy=true and s3_healthy=true, got %+v", m)
	}
	if m.VolumeCount != 12 || m.FileCount != 450 || m.MasterLeader != "127.0.0.1:9334" {
		t.Fatalf("unexpected metrics: %+v", m)
	}
}

func TestParseRedisInfoInt64(t *testing.T) {
	info := "# Clients\r\nconnected_clients:14\r\n# Stats\r\nkeyspace_hits:1200\r\nkeyspace_misses:300\r\n"
	if got := parseRedisInfoInt64(info, "connected_clients:"); got != 14 {
		t.Fatalf("expected 14, got %d", got)
	}
	if got := parseRedisInfoInt64(info, "keyspace_hits:"); got != 1200 {
		t.Fatalf("expected 1200, got %d", got)
	}
}
