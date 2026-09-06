//go:build linux

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// RunWorkerIfRequested is an internal launcher, executed INSIDE the transient
// service. It fails closed if the kernel did not install the requested limits.
func RunWorkerIfRequested() bool {
	if len(os.Args) < 2 || os.Args[1] != "--devspace-bash-worker" {
		return false
	}
	if err := runWorker(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "bash resource guard:", err)
		os.Exit(125)
	}
	return true
}

func runWorker(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("invalid internal worker arguments")
	}
	cpu, err := strconv.ParseFloat(args[0], 64)
	if err != nil || cpu <= 0 {
		return fmt.Errorf("invalid CPU quota")
	}
	memory, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil || memory == 0 {
		return fmt.Errorf("invalid memory quota")
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	cgroup := ""
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::/"); ok {
			cgroup = filepath.Join("/sys/fs/cgroup", path)
			break
		}
	}
	if cgroup == "" {
		return fmt.Errorf("no cgroup v2 membership")
	}
	cpuData, err := os.ReadFile(filepath.Join(cgroup, "cpu.max"))
	if err != nil {
		return fmt.Errorf("CPU controller not delegated: %w", err)
	}
	memData, err := os.ReadFile(filepath.Join(cgroup, "memory.max"))
	if err != nil {
		return fmt.Errorf("memory controller not delegated: %w", err)
	}
	if err := verifyCgroupLimits(string(cpuData), string(memData), cpu, memory); err != nil {
		return err
	}
	exe, err := exec.LookPath(args[2])
	if err != nil {
		return err
	}
	return syscall.Exec(exe, args[2:], os.Environ())
}

func verifyCgroupLimits(cpuMax, memoryMax string, cpuPercent float64, memoryBytes uint64) error {
	parts := strings.Fields(cpuMax)
	if len(parts) != 2 || parts[0] == "max" {
		return fmt.Errorf("CPU limit is not enforced")
	}
	quota, qerr := strconv.ParseFloat(parts[0], 64)
	period, perr := strconv.ParseFloat(parts[1], 64)
	if qerr != nil || perr != nil || period <= 0 || quota <= 0 || quota/period*100 > cpuPercent+0.01 {
		return fmt.Errorf("CPU quota is looser than requested")
	}
	memory, err := strconv.ParseUint(strings.TrimSpace(memoryMax), 10, 64)
	if err != nil || memory == 0 || memory > memoryBytes {
		return fmt.Errorf("memory limit is not enforced as requested")
	}
	return nil
}
