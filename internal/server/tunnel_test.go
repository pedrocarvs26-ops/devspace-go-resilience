package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

// Portable fake tunnel, including output much larger than an OS pipe buffer.
func TestTunnelHelper(t *testing.T) {
	if os.Getenv("DEVSPACE_TUNNEL_HELPER") != "1" {
		return
	}
	mode := os.Getenv("DEVSPACE_TUNNEL_MODE")
	if mode != "no-url" {
		fmt.Fprintln(os.Stdout, "https://test.trycloudflare.com")
		data := bytes.Repeat([]byte("x"), 4096)
		for i := 0; i < 512; i++ {
			_, _ = os.Stdout.Write(data)
			_, _ = os.Stderr.Write(data)
		}
	}
	if mode == "exit" {
		time.Sleep(50 * time.Millisecond)
		os.Exit(17)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
func fakeTunnel(mode string) tunnelProvider {
	return tunnelProvider{name: "fake", startupTimeout: 2 * time.Second, urlPattern: regexp.MustCompile(`https://[a-z-]+\.trycloudflare\.com`),
		command: func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTunnelHelper$")
			cmd.Env = append(os.Environ(), "DEVSPACE_TUNNEL_HELPER=1", "DEVSPACE_TUNNEL_MODE="+mode)
			return cmd
		}}
}
func TestTunnelContinuesDrainingAfterURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := false
	ready, err := runTunnel(ctx, fakeTunnel("exit"), time.Hour, func(context.Context, string) error { return nil }, func(string) { seen = true })
	if !ready || !seen || err == nil || !strings.Contains(err.Error(), "exit status 17") {
		t.Fatalf("ready=%v seen=%v err=%v", ready, seen, err)
	}
	if ctx.Err() != nil {
		t.Fatal("pipe draining deadlocked")
	}
}
func TestTunnelWatchdogStopsHungProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failures := 0
	ready, err := runTunnel(ctx, fakeTunnel("hang"), 20*time.Millisecond, func(context.Context, string) error { failures++; return errors.New("unreachable") }, func(string) {})
	if !ready || failures != 3 || err == nil || !strings.Contains(err.Error(), "three consecutive") {
		t.Fatalf("ready=%v failures=%d err=%v", ready, failures, err)
	}
	if ctx.Err() != nil {
		t.Fatal("watchdog did not reap child")
	}
}
func TestTunnelStartupTimeout(t *testing.T) {
	p := fakeTunnel("no-url")
	p.startupTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, err := runTunnel(ctx, p, time.Second, func(context.Context, string) error { return nil }, func(string) {})
	if ready || err == nil || !strings.Contains(err.Error(), "discovery timeout") {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
}
func TestSupervisorRetriesAndStopsOnCancellation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TunnelReconnectBackoff = "100ms"
	s := &Server{cfg: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := fakeTunnel("exit")
	original := p.command
	var starts atomic.Int32
	p.command = func(ctx context.Context) *exec.Cmd {
		if starts.Add(1) == 3 {
			cancel()
		}
		return original(ctx)
	}
	s.superviseProviders(ctx, []tunnelProvider{p}, func(context.Context, string) error { return nil })
	if starts.Load() != 3 {
		t.Fatalf("starts=%d", starts.Load())
	}
	time.Sleep(150 * time.Millisecond)
	if starts.Load() != 3 {
		t.Fatal("restart after shutdown")
	}
}
func TestBackoffIsBoundedAndCancellable(t *testing.T) {
	for n := 1; n < 100; n++ {
		low := reconnectDelay(time.Second, n, 0)
		high := reconnectDelay(time.Second, n, 1)
		if low <= 0 || high > time.Minute || low > high {
			t.Fatal(low, high)
		}
	}
	if reconnectDelay(time.Second, 3, 1) != 4*time.Second {
		t.Fatal("not exponential")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitTunnelRetry(ctx, time.Hour) {
		t.Fatal("ignored cancellation")
	}
}
func TestTunnelLogBufferIsBounded(t *testing.T) {
	w := &tunnelOutput{pattern: regexp.MustCompile(`https://[a-z]+\.trycloudflare\.com`), urls: make(chan string, 1)}
	_, _ = w.Write([]byte("https://test.trycloudflare.com\n"))
	for i := 0; i < 1000; i++ {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}
	if len(w.partial) > 8192 || len(w.lastLine) > 4096 {
		t.Fatal("unbounded logs")
	}
	select {
	case <-w.urls:
	default:
		t.Fatal("URL missing")
	}
}
func TestHTTPTimeoutConfigAndPublicHealth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HTTPReadTimeout = "7s"
	cfg.HTTPWriteTimeout = "0s"
	cfg.HTTPIdleTimeout = "90s"
	s := &Server{cfg: cfg}
	httpServer := s.newHTTPServer(http.NotFoundHandler())
	if httpServer.ReadTimeout != 7*time.Second || httpServer.WriteTimeout != 0 || httpServer.IdleTimeout != 90*time.Second || httpServer.ReadHeaderTimeout != 7*time.Second {
		t.Fatal("HTTP timeouts not applied")
	}
	for _, body := range []string{`{"ok":true,"name":"devspace-go"}`, `{"ok":false}`, `not JSON`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		err := probeTunnel(context.Background(), upstream.Client(), upstream.URL)
		upstream.Close()
		if (err == nil) != (strings.Contains(body, `"name":"devspace-go"`)) {
			t.Fatalf("health body=%s err=%v", body, err)
		}
	}
}
