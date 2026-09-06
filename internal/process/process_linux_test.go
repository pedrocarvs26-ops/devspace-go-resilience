//go:build linux

package process

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

func TestLinuxNiceAppliedBeforeShell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := config.DefaultBashResourceLimit()
	r.Nice = 15
	var output strings.Builder
	cmd, cleanup, err := Start(ctx, t.TempDir(), "sh", []string{"-c", "ps -o ni= -p $$"}, r, &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	priority, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil || priority < 15 {
		t.Fatalf("nice=%q err=%v", output.String(), err)
	}
}

func TestCgroupVerificationFailsClosed(t *testing.T) {
	if err := verifyCgroupLimits("50000 100000", "1048576", 50, 1048576); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][2]string{{"max 100000", "1048576"}, {"100000 100000", "1048576"}, {"50000 100000", "max"}, {"50000 100000", "2097152"}} {
		if verifyCgroupLimits(values[0], values[1], 50, 1048576) == nil {
			t.Fatalf("accepted unguarded values %v", values)
		}
	}
}

func TestNetworkRatePolicyDetection(t *testing.T) {
	good := []any{map[string]any{"kind": "tbf", "options": map[string]any{"rate": float64(1024)}}}
	if !hasRate(good, "tbf", 1024) || hasRate(good, "tbf", 512) || hasRate(good, "police", 1024) {
		t.Fatal("invalid bandwidth detection")
	}
}

func TestLinuxCancelKillsOrdinaryDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Background sleep inherits the shell's process group.
	cmd, cleanup, err := Start(ctx, dir, "sh", []string{"-c", "sleep 30 & echo $! > child.pid; wait"}, config.DefaultBashResourceLimit(), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var pid int
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	cleanup()
	if pid <= 0 {
		t.Fatal("child was not started")
	}
	// Orphan zombies may remain until PID 1 reaps them, but cannot execute.
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err == nil && !strings.Contains(string(data), ") Z ") && syscall.Kill(pid, 0) == nil {
		t.Fatalf("descendant still running: %s", data)
	}
}
