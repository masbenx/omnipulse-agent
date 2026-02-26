package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// PackageInfo represents an installed package on the system
type PackageInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch,omitempty"`
}

// SystemdServiceInfo represents a systemd service unit
type SystemdServiceInfo struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

// ServerInventoryPayload is sent to POST /api/ingest/server-inventory
type ServerInventoryPayload struct {
	Timestamp       string               `json:"timestamp"`
	Packages        []PackageInfo        `json:"packages"`
	SystemdServices []SystemdServiceInfo `json:"systemd_services"`
}

// collectInventory gathers installed packages and systemd services
func collectInventory() (ServerInventoryPayload, error) {
	payload := ServerInventoryPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	pkgs, err := collectInstalledPackages()
	if err == nil {
		payload.Packages = pkgs
	}

	svcs, err := collectSystemdServices()
	if err == nil {
		payload.SystemdServices = svcs
	}

	return payload, nil
}

// collectInstalledPackages detects the package manager and lists installed packages.
// Supports: dpkg (Debian/Ubuntu), rpm (RHEL/CentOS/Fedora), apk (Alpine).
func collectInstalledPackages() ([]PackageInfo, error) {
	// Try dpkg (Debian/Ubuntu)
	if pkgs, err := collectDpkgPackages(); err == nil {
		return pkgs, nil
	}

	// Try rpm (RHEL/CentOS/Fedora)
	if pkgs, err := collectRpmPackages(); err == nil {
		return pkgs, nil
	}

	// Try apk (Alpine Linux)
	if pkgs, err := collectApkPackages(); err == nil {
		return pkgs, nil
	}

	return nil, fmt.Errorf("no supported package manager found (dpkg/rpm/apk)")
}

// collectDpkgPackages lists installed packages via dpkg-query
func collectDpkgPackages() ([]PackageInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Format: "name\tversion\tarch"
	cmd := exec.CommandContext(ctx, "dpkg-query",
		"-f", "${Package}\t${Version}\t${Architecture}\n",
		"-W",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}

	var packages []PackageInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		pkg := PackageInfo{
			Name:    parts[0],
			Version: parts[1],
		}
		if len(parts) == 3 {
			pkg.Arch = parts[2]
		}
		packages = append(packages, pkg)
	}

	return packages, nil
}

// collectRpmPackages lists installed packages via rpm
func collectRpmPackages() ([]PackageInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Format: "name\tversion\tarch"
	cmd := exec.CommandContext(ctx, "rpm", "-qa",
		"--queryformat", "%{NAME}\t%{VERSION}-%{RELEASE}\t%{ARCH}\n",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rpm: %w", err)
	}

	var packages []PackageInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		pkg := PackageInfo{
			Name:    parts[0],
			Version: parts[1],
		}
		if len(parts) == 3 {
			pkg.Arch = parts[2]
		}
		packages = append(packages, pkg)
	}

	return packages, nil
}

// collectApkPackages lists installed packages via apk (Alpine Linux)
func collectApkPackages() ([]PackageInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "apk", "info", "-v")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("apk: %w", err)
	}

	var packages []PackageInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// apk output format: "name-version" — split on last hyphen before version
		// e.g. "musl-1.2.4-r2" → name=musl, version=1.2.4-r2
		lastDash := strings.LastIndex(line, "-")
		if lastDash <= 0 {
			packages = append(packages, PackageInfo{Name: line, Version: ""})
			continue
		}
		// Find the version part (starts with a digit)
		for i := lastDash; i > 0; i-- {
			if line[i] == '-' {
				versionCandidate := line[i+1:]
				if len(versionCandidate) > 0 && (versionCandidate[0] >= '0' && versionCandidate[0] <= '9') {
					packages = append(packages, PackageInfo{
						Name:    line[:i],
						Version: versionCandidate,
					})
					break
				}
			}
		}
	}

	return packages, nil
}

// collectSystemdServices lists running systemd service units
func collectSystemdServices() ([]SystemdServiceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// List all service units with their status and description
	// Output format: unit-name\tactive-state\tdescription
	cmd := exec.CommandContext(ctx, "systemctl", "list-units",
		"--type=service",
		"--all",
		"--no-pager",
		"--no-legend",
		"--plain",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl: %w", err)
	}

	var services []SystemdServiceInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Fields: UNIT LOAD ACTIVE SUB DESCRIPTION
		// e.g: "ssh.service   loaded active  running OpenBSD Secure Shell server"
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		unitName := fields[0]
		// Skip non-service units that might slip through
		if !strings.HasSuffix(unitName, ".service") {
			continue
		}

		activeState := fields[2] // "active", "inactive", "failed", etc.
		subState := fields[3]    // "running", "exited", "dead", etc.

		status := subState
		if activeState != "active" {
			status = activeState
		}

		description := ""
		if len(fields) > 4 {
			description = strings.Join(fields[4:], " ")
		}

		// Strip .service suffix from name for cleanliness
		name := strings.TrimSuffix(unitName, ".service")

		services = append(services, SystemdServiceInfo{
			Name:        name,
			Status:      status,
			Description: description,
		})
	}

	return services, nil
}

// sendInventoryToBackend collects and sends system inventory
func sendInventoryToBackend(client *http.Client, cfg Config, logger *log.Logger) {
	payload, err := collectInventory()
	if err != nil {
		logger.Printf("inventory collect error: %v", err)
		return
	}

	if err := sendInventory(client, cfg, payload); err != nil {
		logger.Printf("inventory ingest failed: %v", err)
	} else {
		logger.Printf("inventory sent: %d packages, %d services",
			len(payload.Packages), len(payload.SystemdServices))
	}
}

// sendInventory sends the inventory payload to the backend
func sendInventory(client *http.Client, cfg Config, payload ServerInventoryPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := cfg.BaseURL + "/api/ingest/server-inventory"
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
