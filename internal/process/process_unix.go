//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

func Start(ctx context.Context, cwd, name string, args []string, limits config.BashResourceLimit, stdout, stderr io.Writer) (*exec.Cmd, func(), error) {
	if limits.Enabled {
		return nil, nil, fmt.Errorf("hard bashResourceLimit is supported on Linux/systemd and Windows only")
	}
	nice, err := exec.LookPath("nice")
	if err != nil {
		return nil, nil, err
	}
	argv := append([]string{"-n", strconv.Itoa(limits.Nice), name}, args...)
	cmd := exec.CommandContext(ctx, nice, argv...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		})
	}
	cmd.Cancel = func() error { cleanup(); return nil }
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, cleanup, nil
}
