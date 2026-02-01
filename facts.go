package main

import (
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
)

// FactsPayload contains static system information
type FactsPayload struct {
	Hostname       string     `json:"hostname"`
	OSName         string     `json:"os_name"`
	OSVersion      string     `json:"os_version"`
	KernelVersion  string     `json:"kernel_version"`
	CPUModel       string     `json:"cpu_model"`
	CPUCores       int        `json:"cpu_cores"`
	MemTotalBytes  uint64     `json:"mem_total_bytes"`
	Provider       string     `json:"provider,omitempty"`
	Virtualization string     `json:"virtualization,omitempty"`
	Disks          []DiskFact `json:"disks"`
	NICs           []NICFact  `json:"nics"`
	AgentVersion   string     `json:"agent_version"`
}

// DiskFact contains disk partition information
type DiskFact struct {
	Device string `json:"device"`
	Mount  string `json:"mount"`
	FSType string `json:"fstype"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
	Free   uint64 `json:"free"`
}

// NICFact contains network interface information
type NICFact struct {
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`
	MAC    string `json:"mac,omitempty"`
	MTU    int    `json:"mtu"`
	Status string `json:"status"`
}

// collectFacts gathers static system information
func collectFacts() (FactsPayload, error) {
	facts := FactsPayload{
		AgentVersion: Version,
	}

	// Hostname
	hostname, err := os.Hostname()
	if err == nil {
		facts.Hostname = hostname
	}

	// Host info (OS, platform, kernel)
	hostInfo, err := host.Info()
	if err == nil {
		facts.OSName = hostInfo.Platform
		facts.OSVersion = hostInfo.PlatformVersion
		facts.KernelVersion = hostInfo.KernelVersion
		facts.Virtualization = hostInfo.VirtualizationSystem
		if hostInfo.VirtualizationRole == "guest" {
			facts.Provider = detectProvider()
		}
	}

	// CPU info
	cpuInfo, err := cpu.Info()
	if err == nil && len(cpuInfo) > 0 {
		facts.CPUModel = cpuInfo[0].ModelName
	}
	facts.CPUCores = runtime.NumCPU()

	// Memory info
	memInfo, err := mem.VirtualMemory()
	if err == nil {
		facts.MemTotalBytes = memInfo.Total
	}

	// Disk info
	facts.Disks = collectDiskFacts()

	// Network interfaces
	facts.NICs = collectNICFacts()

	return facts, nil
}

// collectDiskFacts retrieves disk partition information
func collectDiskFacts() []DiskFact {
	var disks []DiskFact

	partitions, err := disk.Partitions(false)
	if err != nil {
		return disks
	}

	for _, part := range partitions {
		// Skip certain filesystem types
		if shouldSkipPartition(part.Fstype, part.Mountpoint) {
			continue
		}

		usage, err := disk.Usage(part.Mountpoint)
		if err != nil {
			continue
		}

		disks = append(disks, DiskFact{
			Device: part.Device,
			Mount:  part.Mountpoint,
			FSType: part.Fstype,
			Total:  usage.Total,
			Used:   usage.Used,
			Free:   usage.Free,
		})
	}

	return disks
}

// shouldSkipPartition checks if a partition should be excluded
func shouldSkipPartition(fstype, mountpoint string) bool {
	// Skip virtual/special filesystems
	skipFsTypes := []string{
		"tmpfs", "devtmpfs", "squashfs", "overlay",
		"proc", "sysfs", "devpts", "cgroup", "cgroup2",
		"securityfs", "pstore", "debugfs", "tracefs",
		"configfs", "fusectl", "mqueue", "hugetlbfs",
		"binfmt_misc", "autofs", "efivarfs", "bpf",
	}

	for _, skip := range skipFsTypes {
		if strings.EqualFold(fstype, skip) {
			return true
		}
	}

	// Skip certain mount points
	skipMounts := []string{"/snap/", "/run/", "/sys/", "/proc/", "/dev/"}
	for _, skip := range skipMounts {
		if strings.HasPrefix(mountpoint, skip) {
			return true
		}
	}

	return false
}

// collectNICFacts retrieves network interface information
func collectNICFacts() []NICFact {
	var nics []NICFact

	interfaces, err := gnet.Interfaces()
	if err != nil {
		return nics
	}

	for _, iface := range interfaces {
		// Skip loopback
		if isLoopbackIface(iface) {
			continue
		}

		nic := NICFact{
			Name:   iface.Name,
			MAC:    iface.HardwareAddr,
			MTU:    iface.MTU,
			Status: getInterfaceStatus(iface.Flags),
		}

		// Get first non-loopback IP
		for _, addr := range iface.Addrs {
			ip := extractIP(addr.Addr)
			if ip != "" && !strings.HasPrefix(ip, "127.") && !strings.HasPrefix(ip, "::1") {
				nic.IP = ip
				break
			}
		}

		nics = append(nics, nic)
	}

	return nics
}

// isLoopbackIface checks if interface is loopback
func isLoopbackIface(iface gnet.InterfaceStat) bool {
	name := strings.ToLower(iface.Name)
	if strings.HasPrefix(name, "lo") {
		return true
	}
	for _, flag := range iface.Flags {
		if strings.EqualFold(flag, "loopback") {
			return true
		}
	}
	return false
}

// getInterfaceStatus returns UP or DOWN based on flags
func getInterfaceStatus(flags []string) string {
	for _, flag := range flags {
		if strings.EqualFold(flag, "up") {
			return "up"
		}
	}
	return "down"
}

// extractIP extracts IP from CIDR notation
func extractIP(addr string) string {
	if idx := strings.Index(addr, "/"); idx > 0 {
		return addr[:idx]
	}
	return addr
}

// detectProvider attempts to detect cloud provider
func detectProvider() string {
	// Check common cloud provider DMI strings
	dmiFiles := []string{
		"/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/board_vendor",
		"/sys/class/dmi/id/product_name",
	}

	for _, file := range dmiFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		content := strings.ToLower(strings.TrimSpace(string(data)))

		if strings.Contains(content, "amazon") || strings.Contains(content, "ec2") {
			return "AWS"
		}
		if strings.Contains(content, "google") {
			return "GCP"
		}
		if strings.Contains(content, "microsoft") || strings.Contains(content, "azure") {
			return "Azure"
		}
		if strings.Contains(content, "digitalocean") {
			return "DigitalOcean"
		}
		if strings.Contains(content, "linode") {
			return "Linode"
		}
		if strings.Contains(content, "vultr") {
			return "Vultr"
		}
		if strings.Contains(content, "hetzner") {
			return "Hetzner"
		}
	}

	return ""
}
