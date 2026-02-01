package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

// Version is set at build time via ldflags:
// go build -ldflags "-X main.Version=v1.2.42"
var Version = "dev"

type Config struct {
	BaseURL  string
	Token    string
	Interval time.Duration
	Timeout  time.Duration
}

type MetricPayload struct {
	Timestamp string  `json:"timestamp"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	Disk      float64 `json:"disk"`
	NetIn     int64   `json:"net_in"`
	NetOut    int64   `json:"net_out"`
}

type NetIfaceMetric struct {
	Iface      string `json:"iface"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
	PacketsIn  int64  `json:"packets_in"`
	PacketsOut int64  `json:"packets_out"`
	ErrorsIn   int64  `json:"errors_in"`
	ErrorsOut  int64  `json:"errors_out"`
}

type NetIfacePayload struct {
	Timestamp  string           `json:"timestamp"`
	Interfaces []NetIfaceMetric `json:"interfaces"`
}

type NetTotals struct {
	In  uint64
	Out uint64
}

const (
	serviceName        = "omnipulse-agent"
	serviceDisplayName = "OmniPulse Agent"
	serviceDescription = "OmniPulse Agent metrics collector"
)

type program struct {
	cfg    Config
	logger *log.Logger
	stopCh chan struct{}
}

func (p *program) Start(s service.Service) error {
	if p.stopCh == nil {
		p.stopCh = make(chan struct{})
	}
	go runAgent(p.cfg, p.logger, p.stopCh)
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.stopCh != nil {
		close(p.stopCh)
	}
	return nil
}

func main() {
	logger := log.New(os.Stdout, "omnipulse-agent: ", log.LstdFlags)
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		if cmd == "version" || cmd == "-version" || cmd == "--version" || cmd == "-v" {
			fmt.Printf("omnipulse-agent %s\n", Version)
			return
		}
		if cmd == "run" {
			cfg, err := loadConfig(os.Args[2:])
			if err != nil {
				logger.Fatal(err)
			}
			runAgent(cfg, logger, nil)
			return
		}
		if isServiceCommand(cmd) {
			if err := handleServiceCommand(cmd, os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		}
	}

	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		logger.Fatal(err)
	}

	if service.Interactive() {
		runAgent(cfg, logger, nil)
		return
	}

	prg := &program{cfg: cfg, logger: logger, stopCh: make(chan struct{})}
	svcCfg := &service.Config{
		Name:        serviceName,
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
	}
	svc, err := service.New(prg, svcCfg)
	if err != nil {
		logger.Fatal(err)
	}
	if err := svc.Run(); err != nil {
		logger.Fatal(err)
	}
}

func isServiceCommand(cmd string) bool {
	switch cmd {
	case "install", "start", "stop", "restart", "uninstall":
		return true
	default:
		return false
	}
}

func handleServiceCommand(cmd string, args []string, logger *log.Logger) error {
	cfg, err := buildServiceConfig(cmd, args)
	if err != nil {
		return err
	}

	prg := &program{logger: logger, stopCh: make(chan struct{})}
	if cfg.program != nil {
		prg = cfg.program
	}

	svc, err := service.New(prg, cfg.svc)
	if err != nil {
		return err
	}

	return service.Control(svc, cmd)
}

type serviceConfig struct {
	svc     *service.Config
	program *program
}

func buildServiceConfig(cmd string, args []string) (*serviceConfig, error) {
	svcCfg := &service.Config{
		Name:        serviceName,
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
	}

	if cmd == "install" {
		cfg, err := loadConfig(args)
		if err != nil {
			return nil, err
		}
		svcCfg.Arguments = buildRunArgs(cfg)
		return &serviceConfig{
			svc: svcCfg,
			program: &program{
				cfg:    cfg,
				logger: log.New(os.Stdout, "omnipulse-agent: ", log.LstdFlags),
				stopCh: make(chan struct{}),
			},
		}, nil
	}

	return &serviceConfig{svc: svcCfg}, nil
}

func buildRunArgs(cfg Config) []string {
	args := []string{
		"run",
		"--url", cfg.BaseURL,
		"--token", cfg.Token,
	}
	if cfg.Interval > 0 {
		args = append(args, "--interval", strconv.Itoa(int(cfg.Interval.Seconds())))
	}
	return args
}

func runAgent(cfg Config, logger *log.Logger, stopCh <-chan struct{}) {
	logger.Printf("starting omnipulse-agent %s interval=%s url=%s", Version, cfg.Interval, cfg.BaseURL)

	client := &http.Client{Timeout: cfg.Timeout}
	prevNet := NetTotals{}
	hasPrev := false
	prevIfaces := map[string]gnet.IOCountersStat{}
	hasPrevIfaces := false
	failCount := 0

	for {
		if stopCh != nil {
			select {
			case <-stopCh:
				logger.Println("stopping")
				return
			default:
			}
		}

		started := time.Now()
		payload, netTotals, netOK, warn := collectMetrics(prevNet, hasPrev)
		if warn != nil {
			logger.Printf("collect warning: %v", warn)
		}

		if err := sendMetrics(client, cfg, payload); err != nil {
			failCount++
			logger.Printf("ingest failed: %v", err)
		} else {
			failCount = 0
			if netOK {
				prevNet = netTotals
				hasPrev = true
			}
		}

		ifaceMetrics, nextIfaces, ifaceOK, ifaceWarn := collectIfaceMetrics(prevIfaces, hasPrevIfaces)
		if ifaceWarn != nil {
			logger.Printf("collect iface warning: %v", ifaceWarn)
		}
		if ifaceOK && len(ifaceMetrics) > 0 {
			if err := sendNetworkMetrics(client, cfg, payload.Timestamp, ifaceMetrics); err != nil {
				logger.Printf("network ingest failed: %v", err)
			}
		}
		if len(nextIfaces) > 0 {
			prevIfaces = nextIfaces
			hasPrevIfaces = true
		}

		sleepFor := nextSleep(cfg.Interval, failCount)
		elapsed := time.Since(started)
		wait := sleepFor - elapsed
		if wait <= 0 {
			continue
		}
		if stopCh == nil {
			time.Sleep(wait)
			continue
		}

		timer := time.NewTimer(wait)
		select {
		case <-stopCh:
			timer.Stop()
			logger.Println("stopping")
			return
		case <-timer.C:
		}
	}
}

func loadConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("omnipulse-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flagURL := fs.String("url", "", "Base URL (env OMNIPULSE_URL)")
	flagToken := fs.String("token", "", "Agent token (env AGENT_TOKEN)")
	flagInterval := fs.Int("interval", 0, "Interval in seconds (env INTERVAL_SECONDS)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	baseURL := strings.TrimSpace(firstNonEmpty(*flagURL, os.Getenv("OMNIPULSE_URL")))
	token := strings.TrimSpace(firstNonEmpty(*flagToken, os.Getenv("AGENT_TOKEN")))
	if baseURL == "" {
		return Config{}, errors.New("OMNIPULSE_URL is required")
	}
	if token == "" {
		return Config{}, errors.New("AGENT_TOKEN is required")
	}

	intervalSeconds := 10
	if *flagInterval > 0 {
		intervalSeconds = *flagInterval
	} else if raw := strings.TrimSpace(os.Getenv("INTERVAL_SECONDS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("invalid INTERVAL_SECONDS: %q", raw)
		}
		intervalSeconds = parsed
	}

	return Config{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Token:    token,
		Interval: time.Duration(intervalSeconds) * time.Second,
		Timeout:  10 * time.Second,
	}, nil
}

func collectMetrics(prev NetTotals, hasPrev bool) (MetricPayload, NetTotals, bool, error) {
	var warnings []string

	cpuPct, err := readCPU()
	if err != nil {
		warnings = append(warnings, "cpu:"+err.Error())
	}

	memPct, err := readMem()
	if err != nil {
		warnings = append(warnings, "mem:"+err.Error())
	}

	diskPct, err := readDisk()
	if err != nil {
		warnings = append(warnings, "disk:"+err.Error())
	}

	netTotals, netOK, netErr := readNetTotals()
	if netErr != nil {
		warnings = append(warnings, "net:"+netErr.Error())
	}

	netIn := int64(0)
	netOut := int64(0)
	if netOK && hasPrev {
		netIn, netOut = netDelta(prev, netTotals)
	}

	payload := MetricPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CPU:       cpuPct,
		Mem:       memPct,
		Disk:      diskPct,
		NetIn:     netIn,
		NetOut:    netOut,
	}

	if len(warnings) > 0 {
		return payload, netTotals, netOK, errors.New(strings.Join(warnings, "; "))
	}
	return payload, netTotals, netOK, nil
}

func sendMetrics(client *http.Client, cfg Config, payload MetricPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := cfg.BaseURL + "/api/ingest/server-metrics"
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

func sendNetworkMetrics(client *http.Client, cfg Config, timestamp string, ifaces []NetIfaceMetric) error {
	if len(ifaces) == 0 {
		return nil
	}

	payload := NetIfacePayload{
		Timestamp:  timestamp,
		Interfaces: ifaces,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := cfg.BaseURL + "/api/ingest/server-network"
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

func readCPU() (float64, error) {
	values, err := cpu.Percent(0, false)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, errors.New("cpu percent empty")
	}
	return values[0], nil
}

func readMem() (float64, error) {
	stats, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}
	return stats.UsedPercent, nil
}

func readDisk() (float64, error) {
	stats, err := disk.Usage("/")
	if err != nil {
		return 0, err
	}
	return stats.UsedPercent, nil
}

func readNetTotals() (NetTotals, bool, error) {
	stats, err := gnet.IOCounters(true)
	if err != nil {
		return NetTotals{}, false, err
	}
	total := NetTotals{}
	for _, stat := range stats {
		if isLoopback(stat.Name) {
			continue
		}
		total.In += stat.BytesRecv
		total.Out += stat.BytesSent
	}
	return total, true, nil
}

func collectIfaceMetrics(prev map[string]gnet.IOCountersStat, hasPrev bool) ([]NetIfaceMetric, map[string]gnet.IOCountersStat, bool, error) {
	stats, err := gnet.IOCounters(true)
	if err != nil {
		return nil, prev, false, err
	}

	next := make(map[string]gnet.IOCountersStat, len(stats))
	for _, stat := range stats {
		if isLoopback(stat.Name) {
			continue
		}
		next[stat.Name] = stat
	}

	if !hasPrev {
		return nil, next, false, nil
	}

	metrics := make([]NetIfaceMetric, 0, len(next))
	for name, curr := range next {
		old, ok := prev[name]
		if !ok {
			continue
		}
		bytesIn, ok1 := safeDelta(old.BytesRecv, curr.BytesRecv)
		bytesOut, ok2 := safeDelta(old.BytesSent, curr.BytesSent)
		packetsIn, ok3 := safeDelta(old.PacketsRecv, curr.PacketsRecv)
		packetsOut, ok4 := safeDelta(old.PacketsSent, curr.PacketsSent)
		errorsIn, ok5 := safeDelta(old.Errin, curr.Errin)
		errorsOut, ok6 := safeDelta(old.Errout, curr.Errout)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
			continue
		}
		metrics = append(metrics, NetIfaceMetric{
			Iface:      name,
			BytesIn:    bytesIn,
			BytesOut:   bytesOut,
			PacketsIn:  packetsIn,
			PacketsOut: packetsOut,
			ErrorsIn:   errorsIn,
			ErrorsOut:  errorsOut,
		})
	}

	return metrics, next, len(metrics) > 0, nil
}

func safeDelta(prev, curr uint64) (int64, bool) {
	if curr < prev {
		return 0, false
	}
	return int64(curr - prev), true
}

func isLoopback(name string) bool {
	normalized := strings.ToLower(name)
	return strings.HasPrefix(normalized, "lo")
}

func netDelta(prev, curr NetTotals) (int64, int64) {
	if curr.In < prev.In || curr.Out < prev.Out {
		return 0, 0
	}
	return int64(curr.In - prev.In), int64(curr.Out - prev.Out)
}

func nextSleep(interval time.Duration, failCount int) time.Duration {
	if failCount <= 0 {
		return interval
	}
	backoff := interval * time.Duration(1<<minInt(failCount, 4))
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	if backoff < interval {
		backoff = interval
	}
	return backoff
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
