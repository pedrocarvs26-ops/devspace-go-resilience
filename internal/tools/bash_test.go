package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

func jobManagerForTest(t *testing.T) *JobManager {
	t.Helper()
	c := config.DefaultConfig()
	c.BashJobs.MaxConcurrent = 1
	m := NewJobManager(c)
	t.Cleanup(m.Close)
	return m
}
func waitJob(t *testing.T, m *JobManager, id string) BashOutput {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out, err := m.Status(BashStatusInput{WorkspaceID: "ws", JobID: id})
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "queued" && out.State != "running" {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not terminate")
	return BashOutput{}
}
func slowCommand() string {
	if runtime.GOOS == "windows" {
		return `Write-Output first; Start-Sleep -Seconds 3; Write-Output last`
	}
	return `printf first; sleep 3; printf last`
}

func TestBashImmediateReturnIncrementalOutputAndCancel(t *testing.T) {
	m := jobManagerForTest(t)
	start := time.Now()
	out, err := m.Submit(BashInput{WorkspaceID: "ws", Command: slowCommand()}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second || out.JobID == "" {
		t.Fatal("submit blocked")
	}
	// Simulate the request disappearing: jobs do not belong to this context.
	request, cancel := context.WithCancel(context.Background())
	cancel()
	_ = request
	deadline := time.Now().Add(2500 * time.Millisecond)
	seen := false
	for time.Now().Before(deadline) {
		status, err := m.Status(BashStatusInput{WorkspaceID: "ws", JobID: out.JobID})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(status.Result, "first") {
			seen = true
			if status.State != "running" {
				t.Fatal("output wasn't incremental")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !seen {
		t.Fatal("no incremental output")
	}
	if _, err := m.Submit(BashInput{WorkspaceID: "ws", Command: slowCommand()}, t.TempDir()); err == nil {
		t.Fatal("concurrency cap ignored")
	}
	if _, err := m.Status(BashStatusInput{WorkspaceID: "other", JobID: out.JobID}); err == nil {
		t.Fatal("workspace ownership ignored")
	}
	if _, err := m.Cancel(BashCancelInput{WorkspaceID: "ws", JobID: out.JobID}); err != nil {
		t.Fatal(err)
	}
	if final := waitJob(t, m, out.JobID); final.State != "cancelled" {
		t.Fatalf("state=%s: %s", final.State, final.Error)
	}
}

func TestBashTimeoutAndNonzeroExit(t *testing.T) {
	m := jobManagerForTest(t)
	out, err := m.Submit(BashInput{WorkspaceID: "ws", Command: slowCommand(), Timeout: 1}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if final := waitJob(t, m, out.JobID); final.State != "timed_out" {
		t.Fatalf("state=%s: %s", final.State, final.Error)
	}
	// Job completion and slot release may be separated by a scheduler turn.
	m.mu.Lock()
	m.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	out, err = m.Submit(BashInput{WorkspaceID: "ws", Command: "exit 7"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, m, out.JobID)
	if final.State != "failed" || final.ExitCode == nil || *final.ExitCode != 7 {
		t.Fatalf("bad exit: %+v", final)
	}
}

func TestJobOutputBoundedLosslessAndCursorPagination(t *testing.T) {
	j := &bashJob{id: "id", workspaceID: "ws", state: "running", capacity: 4096}
	w := jobWriter{j, "stdout"}
	data := bytes.Repeat([]byte{0xff, 0, 'x'}, 10000)
	if n, err := w.Write(data); err != nil || n != len(data) {
		t.Fatal(n, err)
	}
	if j.bytes > 4096 || len(j.chunks) > 512 {
		t.Fatal("unbounded output")
	}
	m := &JobManager{jobs: map[string]*bashJob{"id": j}}
	var actual []byte
	cursor := int64(0)
	dropped := int64(0)
	for {
		out, err := m.Status(BashStatusInput{WorkspaceID: "ws", JobID: "id", Offset: cursor, MaxBytes: 257})
		if err != nil {
			t.Fatal(err)
		}
		if cursor == 0 {
			dropped = out.DroppedBytes
		}
		for _, chunk := range out.Chunks {
			b, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
			if err != nil {
				t.Fatal(err)
			}
			actual = append(actual, b...)
		}
		if out.NextOffset <= cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = out.NextOffset
		if !out.HasMore {
			break
		}
	}
	if dropped == 0 || !bytes.Equal(actual, data[dropped:]) || cursor != int64(len(data)) {
		t.Fatal("cursor data lost or duplicated")
	}
	if _, err := m.Status(BashStatusInput{WorkspaceID: "ws", JobID: "id", Offset: cursor + 1}); err == nil {
		t.Fatal("future cursor accepted")
	}
}

func TestJobOutputConcurrentReadersAndWriters(t *testing.T) {
	j := &bashJob{id: "id", workspaceID: "ws", state: "running", capacity: 4096}
	m := &JobManager{jobs: map[string]*bashJob{"id": j}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				_, _ = (jobWriter{j, "stdout"}).Write([]byte("x"))
				_, _ = m.Status(BashStatusInput{WorkspaceID: "ws", JobID: "id"})
			}
		}()
	}
	wg.Wait()
	if len(j.chunks) > 512 || j.bytes > 4096 {
		t.Fatal("retention cap violated")
	}
}

func TestBashWorkingDirectoryEscapeRejected(t *testing.T) {
	m := jobManagerForTest(t)
	root := t.TempDir()
	if _, err := m.Submit(BashInput{WorkspaceID: "ws", Command: "echo unsafe", WorkingDirectory: ".."}, root); err == nil {
		t.Fatal("parent escape accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Submit(BashInput{WorkspaceID: "ws", Command: "echo unsafe", WorkingDirectory: "outside"}, root); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}

func TestJobRetentionAndShutdown(t *testing.T) {
	m := jobManagerForTest(t)
	m.mu.Lock()
	m.jobs["expired"] = &bashJob{id: "expired", state: "completed", finished: time.Now().Add(-time.Hour)}
	m.pruneLocked(time.Now())
	_, exists := m.jobs["expired"]
	m.mu.Unlock()
	if exists {
		t.Fatal("expired job retained")
	}
	out, err := m.Submit(BashInput{WorkspaceID: "ws", Command: slowCommand()}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if final := waitJob(t, m, out.JobID); final.State != "cancelled" {
		t.Fatalf("shutdown state=%s", final.State)
	}
	if _, err := m.Submit(BashInput{WorkspaceID: "ws", Command: "echo nope"}, t.TempDir()); err == nil {
		t.Fatal("submit after shutdown")
	}
}
