package main

import (
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v3/net"
)

// --- loadConfig Tests ---

func TestLoadConfig_RequiresURL(t *testing.T) {
	_, err := loadConfig([]string{"-token", "abc"})
	if err == nil {
		t.Fatal("expected error when OMNIPULSE_URL is missing")
	}
}

func TestLoadConfig_RequiresToken(t *testing.T) {
	_, err := loadConfig([]string{"-url", "http://localhost"})
	if err == nil {
		t.Fatal("expected error when AGENT_TOKEN is missing")
	}
}

func TestLoadConfig_Success(t *testing.T) {
	cfg, err := loadConfig([]string{"-url", "http://localhost:8080", "-token", "test-token"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("expected BaseURL 'http://localhost:8080', got %q", cfg.BaseURL)
	}
	if cfg.Token != "test-token" {
		t.Errorf("expected Token 'test-token', got %q", cfg.Token)
	}
	if cfg.Interval != 10*time.Second {
		t.Errorf("expected default Interval 10s, got %v", cfg.Interval)
	}
	if cfg.Timeout != 10*time.Second {
		t.Errorf("expected Timeout 10s, got %v", cfg.Timeout)
	}
}

func TestLoadConfig_CustomInterval(t *testing.T) {
	cfg, err := loadConfig([]string{"-url", "http://localhost", "-token", "tok", "-interval", "30"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("expected Interval 30s, got %v", cfg.Interval)
	}
}

func TestLoadConfig_TrimsTrailingSlash(t *testing.T) {
	cfg, err := loadConfig([]string{"-url", "http://localhost:8080/", "-token", "tok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("expected trailing slash removed, got %q", cfg.BaseURL)
	}
}

// --- firstNonEmpty Tests ---

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name   string
		input  []string
		expect string
	}{
		{"first non-empty", []string{"a", "b"}, "a"},
		{"skips empty", []string{"", "b"}, "b"},
		{"skips whitespace", []string{"  ", "b"}, "b"},
		{"all empty", []string{"", "", ""}, ""},
		{"no args", []string{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := firstNonEmpty(tt.input...)
			if result != tt.expect {
				t.Errorf("expected %q, got %q", tt.expect, result)
			}
		})
	}
}

// --- minInt Tests ---

func TestMinInt(t *testing.T) {
	tests := []struct {
		a, b, expect int
	}{
		{1, 2, 1},
		{5, 3, 3},
		{0, 0, 0},
		{-1, 1, -1},
	}
	for _, tt := range tests {
		result := minInt(tt.a, tt.b)
		if result != tt.expect {
			t.Errorf("minInt(%d, %d) = %d, expected %d", tt.a, tt.b, result, tt.expect)
		}
	}
}

// --- nextSleep (exponential backoff) Tests ---

func TestNextSleep_NoFailure(t *testing.T) {
	interval := 10 * time.Second
	result := nextSleep(interval, 0)
	if result != interval {
		t.Errorf("expected %v, got %v", interval, result)
	}
}

func TestNextSleep_ExponentialBackoff(t *testing.T) {
	interval := 10 * time.Second

	tests := []struct {
		failCount int
		maxExpect time.Duration
	}{
		{1, 20 * time.Second},  // 10 * 2^1 = 20
		{2, 40 * time.Second},  // 10 * 2^2 = 40
		{3, 60 * time.Second},  // 10 * 2^3 = 80 → capped at 60
		{4, 60 * time.Second},  // capped at 60
		{10, 60 * time.Second}, // capped at 60
	}

	for _, tt := range tests {
		result := nextSleep(interval, tt.failCount)
		if result > 60*time.Second {
			t.Errorf("nextSleep(10s, %d) = %v, should be capped at 60s", tt.failCount, result)
		}
		if result < interval {
			t.Errorf("nextSleep(10s, %d) = %v, should be >= interval", tt.failCount, result)
		}
	}
}

func TestNextSleep_NegativeFails(t *testing.T) {
	interval := 10 * time.Second
	result := nextSleep(interval, -1)
	if result != interval {
		t.Errorf("expected %v for negative failCount, got %v", interval, result)
	}
}

// --- safeDelta Tests ---

func TestSafeDelta_Normal(t *testing.T) {
	delta, ok := safeDelta(100, 200)
	if !ok {
		t.Error("expected ok=true")
	}
	if delta != 100 {
		t.Errorf("expected delta 100, got %d", delta)
	}
}

func TestSafeDelta_Overflow(t *testing.T) {
	delta, ok := safeDelta(200, 100)
	if ok {
		t.Error("expected ok=false for overflow (counter reset)")
	}
	if delta != 0 {
		t.Errorf("expected delta 0 for overflow, got %d", delta)
	}
}

func TestSafeDelta_Equal(t *testing.T) {
	delta, ok := safeDelta(50, 50)
	if !ok {
		t.Error("expected ok=true for equal values")
	}
	if delta != 0 {
		t.Errorf("expected delta 0, got %d", delta)
	}
}

// --- netDelta Tests ---

func TestNetDelta_Normal(t *testing.T) {
	prev := NetTotals{In: 1000, Out: 500}
	curr := NetTotals{In: 2000, Out: 1500}

	inDelta, outDelta := netDelta(prev, curr)
	if inDelta != 1000 {
		t.Errorf("expected in delta 1000, got %d", inDelta)
	}
	if outDelta != 1000 {
		t.Errorf("expected out delta 1000, got %d", outDelta)
	}
}

func TestNetDelta_Overflow(t *testing.T) {
	prev := NetTotals{In: 2000, Out: 500}
	curr := NetTotals{In: 1000, Out: 1500}

	inDelta, outDelta := netDelta(prev, curr)
	if inDelta != 0 || outDelta != 0 {
		t.Errorf("expected 0,0 on overflow, got %d,%d", inDelta, outDelta)
	}
}

// --- isLoopback Tests ---

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		name   string
		expect bool
	}{
		{"lo", true},
		{"lo0", true},
		{"loopback", true},
		{"eth0", false},
		{"wlan0", false},
		{"docker0", false},
	}

	for _, tt := range tests {
		result := isLoopback(tt.name)
		if result != tt.expect {
			t.Errorf("isLoopback(%q) = %v, expected %v", tt.name, result, tt.expect)
		}
	}
}

// --- isServiceCommand Tests ---

func TestIsServiceCommand(t *testing.T) {
	valid := []string{"install", "start", "stop", "restart", "uninstall"}
	for _, cmd := range valid {
		if !isServiceCommand(cmd) {
			t.Errorf("expected %q to be a service command", cmd)
		}
	}

	invalid := []string{"run", "test", "help", "version", ""}
	for _, cmd := range invalid {
		if isServiceCommand(cmd) {
			t.Errorf("expected %q to NOT be a service command", cmd)
		}
	}
}

// --- facts.go Pure Logic Tests ---

func TestShouldSkipPartition(t *testing.T) {
	tests := []struct {
		fstype     string
		mountpoint string
		expect     bool
	}{
		// Virtual filesystems — should be skipped
		{"tmpfs", "/tmp", true},
		{"devtmpfs", "/dev", true},
		{"squashfs", "/snap/core18", true},
		{"proc", "/proc", true},
		{"sysfs", "/sys", true},
		{"cgroup2", "/sys/fs/cgroup", true},
		{"overlay", "/var/lib/docker/overlay2", true},

		// Mount point-based skip
		{"ext4", "/snap/something", true},
		{"ext4", "/run/user/1000", true},
		{"ext4", "/sys/kernel", true},
		{"ext4", "/proc/1234", true},
		{"ext4", "/dev/shm", true},

		// Real partitions — should NOT be skipped
		{"ext4", "/", false},
		{"ext4", "/home", false},
		{"xfs", "/data", false},
		{"btrfs", "/var", false},

		// Case-insensitive fstype
		{"TMPFS", "/tmp", true},
		{"Overlay", "/docker", true},
	}

	for _, tt := range tests {
		t.Run(tt.fstype+"_"+tt.mountpoint, func(t *testing.T) {
			result := shouldSkipPartition(tt.fstype, tt.mountpoint)
			if result != tt.expect {
				t.Errorf("shouldSkipPartition(%q, %q) = %v, expected %v", tt.fstype, tt.mountpoint, result, tt.expect)
			}
		})
	}
}

func TestGetInterfaceStatus(t *testing.T) {
	tests := []struct {
		flags  []string
		expect string
	}{
		{[]string{"up", "broadcast", "multicast"}, "up"},
		{[]string{"UP", "BROADCAST"}, "up"},
		{[]string{"broadcast", "multicast"}, "down"},
		{[]string{}, "down"},
		{nil, "down"},
	}

	for _, tt := range tests {
		result := getInterfaceStatus(tt.flags)
		if result != tt.expect {
			t.Errorf("getInterfaceStatus(%v) = %q, expected %q", tt.flags, result, tt.expect)
		}
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"192.168.1.100/24", "192.168.1.100"},
		{"10.0.0.1/8", "10.0.0.1"},
		{"fe80::1/64", "fe80::1"},
		{"192.168.1.100", "192.168.1.100"},
		{"::1", "::1"},
	}

	for _, tt := range tests {
		result := extractIP(tt.input)
		if result != tt.expect {
			t.Errorf("extractIP(%q) = %q, expected %q", tt.input, result, tt.expect)
		}
	}
}

func TestIsLoopbackIface(t *testing.T) {
	tests := []struct {
		name   string
		iface  gnet.InterfaceStat
		expect bool
	}{
		{
			"lo by name",
			gnet.InterfaceStat{Name: "lo"},
			true,
		},
		{
			"lo0 by name",
			gnet.InterfaceStat{Name: "lo0"},
			true,
		},
		{
			"loopback by flag",
			gnet.InterfaceStat{Name: "eth0", Flags: []string{"loopback"}},
			true,
		},
		{
			"eth0 not loopback",
			gnet.InterfaceStat{Name: "eth0", Flags: []string{"up", "broadcast"}},
			false,
		},
		{
			"wlan0 not loopback",
			gnet.InterfaceStat{Name: "wlan0"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLoopbackIface(tt.iface)
			if result != tt.expect {
				t.Errorf("isLoopbackIface(%q) = %v, expected %v", tt.iface.Name, result, tt.expect)
			}
		})
	}
}

// --- MetricPayload / Config struct tests ---

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		BaseURL:  "https://api.omnipulse.cloud",
		Token:    "abc123",
		Interval: 10 * time.Second,
		Timeout:  5 * time.Second,
	}

	if cfg.BaseURL != "https://api.omnipulse.cloud" {
		t.Errorf("unexpected BaseURL: %q", cfg.BaseURL)
	}
	if cfg.Token != "abc123" {
		t.Errorf("unexpected Token: %q", cfg.Token)
	}
}

func TestMetricPayload_Fields(t *testing.T) {
	p := MetricPayload{
		Timestamp: "2025-01-01T00:00:00Z",
		CPU:       45.5,
		Mem:       70.2,
		Disk:      30.0,
		NetIn:     1024,
		NetOut:    2048,
	}

	if p.CPU != 45.5 {
		t.Errorf("unexpected CPU: %f", p.CPU)
	}
	if p.NetIn != 1024 {
		t.Errorf("unexpected NetIn: %d", p.NetIn)
	}
}
