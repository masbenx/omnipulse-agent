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

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

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

type NetTotals struct {
	In  uint64
	Out uint64
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	logger := log.New(os.Stdout, "omnipulse-agent: ", log.LstdFlags)
	logger.Printf("starting interval=%s url=%s", cfg.Interval, cfg.BaseURL)

	client := &http.Client{Timeout: cfg.Timeout}
	prevNet := NetTotals{}
	hasPrev := false
	failCount := 0

	for {
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

		sleepFor := nextSleep(cfg.Interval, failCount)
		elapsed := time.Since(started)
		if elapsed < sleepFor {
			time.Sleep(sleepFor - elapsed)
		}
	}
}

func loadConfig() (Config, error) {
	flagURL := flag.String("url", "", "Base URL (env OMNIPULSE_URL)")
	flagToken := flag.String("token", "", "Agent token (env AGENT_TOKEN)")
	flagInterval := flag.Int("interval", 0, "Interval in seconds (env INTERVAL_SECONDS)")
	flag.Parse()

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
