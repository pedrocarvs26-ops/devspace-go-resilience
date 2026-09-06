//go:build windows

package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/snakex21/devspace-go/internal/config"
	"golang.org/x/sys/windows"
)

// Use x/sys native layouts for Job Object extended limits.
type cpuRate struct{ Flags, Rate uint32 }
type netRate struct {
	MaxBandwidth uint64
	Flags        uint32
	DSCP         uint8
}

var (
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	createJob     = kernel32.NewProc("CreateJobObjectW")
	setJobInfo    = kernel32.NewProc("SetInformationJobObject")
	assignJob     = kernel32.NewProc("AssignProcessToJobObject")
	terminateJob  = kernel32.NewProc("TerminateJobObject")
	openProcess   = kernel32.NewProc("OpenProcess")
	closeHandle   = kernel32.NewProc("CloseHandle")
	setPriority   = kernel32.NewProc("SetPriorityClass")
	resumeProcess = syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess")
)

func setJob(job uintptr, class uintptr, value unsafe.Pointer, size uintptr) error {
	ok, _, err := setJobInfo.Call(job, class, uintptr(value), size)
	if ok == 0 {
		return fmt.Errorf("SetInformationJobObject(%d): %w", class, err)
	}
	return nil
}

func Start(ctx context.Context, cwd, name string, args []string, limits config.BashResourceLimit, stdout, stderr io.Writer) (*exec.Cmd, func(), error) {
	job, _, err := createJob.Call(0, 0)
	if job == 0 {
		return nil, nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	var once sync.Once
	var setupMu sync.Mutex
	cleanup := func() {
		once.Do(func() { setupMu.Lock(); defer setupMu.Unlock(); terminateJob.Call(job, 1); closeHandle.Call(job) })
	}
	fail := func(err error) (*exec.Cmd, func(), error) { cleanup(); return nil, nil, err }
	// KILL_ON_JOB_CLOSE; descendants inherit membership, with no breakaway permission.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: 0x2000 | 0x8, ActiveProcessLimit: 256}}
	if limits.Enabled {
		bytes := uint64(limits.MemoryMB) * 1024 * 1024
		if uint64(uintptr(bytes)) != bytes {
			return fail(fmt.Errorf("memoryMB exceeds this Windows architecture's address range"))
		}
		info.BasicLimitInformation.LimitFlags |= 0x200 // JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(bytes)
	}
	if err := setJob(job, 9, unsafe.Pointer(&info), unsafe.Sizeof(info)); err != nil {
		return fail(err)
	}
	if limits.Enabled {
		cpu := cpuRate{Flags: 1 | 4, Rate: uint32(limits.CPUPercent) * 100}
		if err := setJob(job, 15, unsafe.Pointer(&cpu), unsafe.Sizeof(cpu)); err != nil {
			return fail(err)
		}
		if limits.BandwidthBytesPerSecond > 0 {
			// Windows 10+/Server 2016+: limits OUTBOUND bytes/sec, not ingress.
			net := netRate{MaxBandwidth: uint64(limits.BandwidthBytesPerSecond), Flags: 1 | 2}
			if err := setJob(job, 32, unsafe.Pointer(&net), unsafe.Sizeof(net)); err != nil {
				return fail(err)
			}
		}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	// Suspend before the first user instruction: no fork/child escape window before assignment.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x4 | 0x4000 | 0x200}
	cmd.WaitDelay = 2 * time.Second
	// Until assignment succeeds cancellation also needs to kill the suspended process.
	cmd.Cancel = func() error {
		cleanup()
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	startedFail := func(err error) (*exec.Cmd, func(), error) {
		_ = cmd.Process.Kill()
		cleanup()
		_ = cmd.Wait()
		return nil, nil, err
	}
	setupErr := func() error {
		setupMu.Lock()
		defer setupMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		handle, _, err := openProcess.Call(0x100|0x200|0x800|0x1, 0, uintptr(cmd.Process.Pid))
		if handle == 0 {
			return fmt.Errorf("OpenProcess: %w", err)
		}
		defer closeHandle.Call(handle)
		ok, _, err := assignJob.Call(job, handle)
		if ok == 0 {
			return fmt.Errorf("AssignProcessToJobObject (nested jobs may be restricted): %w", err)
		}
		ok, _, err = setPriority.Call(handle, 0x4000)
		if ok == 0 {
			return fmt.Errorf("SetPriorityClass: %w", err)
		}
		status, _, _ := resumeProcess.Call(handle)
		if status != 0 {
			return fmt.Errorf("NtResumeProcess failed: NTSTATUS %#x", status)
		}
		return nil
	}()
	if setupErr != nil {
		return startedFail(setupErr)
	}
	return cmd, cleanup, nil
}
