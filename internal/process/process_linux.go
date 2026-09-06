//go:build linux

// Package process starts shell children with lower priority and bounded lifetime.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/config"
)

// Start applies priority before the shell executes. Enabled hard limits fail closed.
// cleanup MUST run after Wait; it also kills descendants left behind by the shell.
func Start(ctx context.Context, cwd, name string, args []string, limits config.BashResourceLimit, stdout, stderr io.Writer) (*exec.Cmd, func(), error) {
	argv := append([]string{name}, args...)
	if ionice, err := exec.LookPath("ionice"); err == nil {
		argv = append([]string{ionice, "-c", "3", "--"}, argv...)
	} else {
		log.Warn().Msg("bash_ionice_unavailable")
	}
	if nice, err := exec.LookPath("nice"); err == nil {
		argv = append([]string{nice, "-n", strconv.Itoa(limits.Nice), "--"}, argv...)
	} else if limits.Nice != 0 {
		return nil, nil, fmt.Errorf("nice required for bash priority: %w", err)
	}
	unit := ""
	var systemctl string
	if limits.Enabled {
		systemdRun, err := exec.LookPath("systemd-run")
		if err != nil {
			return nil, nil, fmt.Errorf("hard limits require systemd-run: %w", err)
		}
		systemctl, err = exec.LookPath("systemctl")
		if err != nil {
			return nil, nil, fmt.Errorf("hard limits require systemctl: %w", err)
		}
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
			return nil, nil, fmt.Errorf("hard limits require cgroup v2: %w", err)
		}
		if data, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers"); err != nil || !strings.Contains(" "+strings.TrimSpace(string(data))+" ", " cpu ") || !strings.Contains(" "+strings.TrimSpace(string(data))+" ", " memory ") {
			return nil, nil, fmt.Errorf("hard limits require available cpu and memory cgroup v2 controllers")
		}
		unit = "devspace-bash-" + uuid.NewString() + ".service"
		launch := []string{systemdRun, "--no-ask-password", "--quiet", "--wait", "--pipe", "--collect", "--service-type=exec", "--unit=" + unit,
			"--working-directory=" + cwd, "--property=KillMode=control-group", "--property=TimeoutStopSec=3s",
			"--property=TasksMax=256", "--property=CPUAccounting=yes", "--property=MemoryAccounting=yes", "--property=IOWeight=10", "--property=MemorySwapMax=0",
			fmt.Sprintf("--property=CPUQuota=%d%%", limits.CPUPercent*runtime.NumCPU()),
			fmt.Sprintf("--property=MemoryMax=%d", limits.MemoryMB*1024*1024)}
		if deadline, ok := ctx.Deadline(); ok {
			seconds := max(1, int(time.Until(deadline).Seconds())+1)
			launch = append(launch, fmt.Sprintf("--property=RuntimeMaxSec=%ds", seconds))
		}
		if limits.SystemdUser {
			launch = append(launch, "--user")
		} else {
			launch = append(launch, "--uid="+strconv.Itoa(os.Getuid()), "--gid="+strconv.Itoa(os.Getgid()))
		}
		if limits.BandwidthBytesPerSecond > 0 {
			if err := validateNetwork(ctx, limits); err != nil {
				return nil, nil, err
			}
			launch = append(launch, "--property=NetworkNamespacePath="+limits.NetworkNamespacePath)
			resolver := filepath.Join("/etc/netns", filepath.Base(limits.NetworkNamespacePath), "resolv.conf")
			if _, err := os.Stat(resolver); err != nil {
				return nil, nil, fmt.Errorf("namespace DNS file: %w", err)
			}
			launch = append(launch, "--property=BindReadOnlyPaths="+resolver+":/etc/resolv.conf")
		}
		// --setenv=NAME copies values from the launcher's environment, without putting
		// secrets in its command-line arguments. Preserve Git/SSH/proxy/toolchain config.
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			if envName.MatchString(key) {
				launch = append(launch, "--setenv="+key)
			}
		}
		self, err := os.Executable()
		if err != nil {
			return nil, nil, err
		}
		// Verify the ACTUAL cgroup settings before executing any user command. Some
		// systemd/cgroup setups accept properties without delegated controllers.
		launch = append(launch, "--", self, "--devspace-bash-worker",
			strconv.Itoa(limits.CPUPercent*runtime.NumCPU()), strconv.FormatInt(limits.MemoryMB*1024*1024, 10))
		argv = append(launch, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	var cleanupMu sync.Mutex
	var killOnce sync.Once
	// The service cgroup is not a child process group of systemd-run. Kill both.
	cleanup := func() {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		killOnce.Do(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		})
		if unit != "" {
			stopCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			stopArgs := []string{"--no-ask-password"}
			if limits.SystemdUser {
				stopArgs = append(stopArgs, "--user")
			}
			stopArgs = append(stopArgs, "stop", unit)
			_ = exec.CommandContext(stopCtx, systemctl, stopArgs...).Run()
			cancel()
		}
	}
	cmd.Cancel = func() error { cleanup(); return nil }
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return cmd, cleanup, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)
var namespacePath = regexp.MustCompile(`^/run/netns/[A-Za-z0-9_-]+$`)

// Network shaping is explicitly provisioned by an administrator, not by an MCP
// tool. Verify BOTH host-veth directions before allowing a bandwidth-limited job.
func validateNetwork(ctx context.Context, r config.BashResourceLimit) error {
	if r.SystemdUser || !namespacePath.MatchString(r.NetworkNamespacePath) || !interfaceName.MatchString(r.NetworkInterface) {
		return fmt.Errorf("Linux bandwidth limits require systemdUser=false, /run/netns/NAME and a dedicated networkInterface; see setup-bash-network.sh")
	}
	if _, err := os.Stat(r.NetworkNamespacePath); err != nil {
		return err
	}
	tc, err := exec.LookPath("tc")
	if err != nil {
		return fmt.Errorf("bandwidth verification requires tc: %w", err)
	}
	for _, check := range []struct {
		args []string
		kind string
	}{
		{[]string{"-j", "qdisc", "show", "dev", r.NetworkInterface}, "tbf"},
		{[]string{"-j", "filter", "show", "dev", r.NetworkInterface, "parent", "ffff:"}, "police"},
	} {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		data, err := exec.CommandContext(probeCtx, tc, check.args...).Output()
		cancel()
		if err != nil {
			return fmt.Errorf("read bandwidth policy: %w", err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if !hasRate(value, check.kind, float64(r.BandwidthBytesPerSecond)) {
			return fmt.Errorf("missing %s bandwidth policy <= %d bytes/sec on %s; run administrator setup first", check.kind, r.BandwidthBytesPerSecond, r.NetworkInterface)
		}
	}
	return nil
}

func hasRate(value any, kind string, maxRate float64) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if hasRate(item, kind, maxRate) {
				return true
			}
		}
	case map[string]any:
		if v["kind"] == kind {
			if rate, ok := v["rate"].(float64); ok && rate > 0 && rate <= maxRate {
				return true
			}
			if options, ok := v["options"].(map[string]any); ok {
				if rate, ok := options["rate"].(float64); ok && rate > 0 && rate <= maxRate {
					return true
				}
			}
		}
		for _, item := range v {
			if hasRate(item, kind, maxRate) {
				return true
			}
		}
	}
	return false
}
