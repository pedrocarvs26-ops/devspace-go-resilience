package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/config"
)

type tunnelProvider struct {
	name           string
	command        func(context.Context) *exec.Cmd
	urlPattern     *regexp.Regexp
	startupTimeout time.Duration
	initialURL     string
}

type tunnelOutput struct {
	mu       sync.Mutex
	partial  string
	lastLine string
	pattern  *regexp.Regexp
	urls     chan string
}

// Unlike the old scanners, this writer NEVER stops draining after finding a URL.
// Unterminated/very long log lines cannot allocate unbounded memory or block pipes.
func (w *tunnelOutput) Write(p []byte) (int, error) {
	n := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(p) > 0 {
		size := min(len(p), 4096)
		w.partial += string(p[:size])
		p = p[size:]
		if match := w.pattern.FindString(w.partial); match != "" {
			select {
			case w.urls <- match:
			default:
			}
		}
		if i := strings.LastIndexByte(w.partial, '\n'); i >= 0 {
			lines := strings.TrimSpace(w.partial[:i])
			if len(lines) > 4096 {
				lines = lines[len(lines)-4096:]
			}
			w.lastLine = lines
			w.partial = w.partial[i+1:]
		}
		if len(w.partial) > 8192 {
			w.partial = w.partial[len(w.partial)-4096:]
		}
	}
	return n, nil
}

func (w *tunnelOutput) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastLine + w.partial
}

func localTunnelHost(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1"
	}
	return strings.Trim(host, "[]")
}

func (s *Server) tunnelProviders() []tunnelProvider {
	var providers []tunnelProvider
	host := localTunnelHost(s.cfg.Host)
	origin := "http://" + net.JoinHostPort(host, fmt.Sprint(s.cfg.Port))
	if exe := findCloudflaredExecutable(); exe != "" {
		initialURL := ""
		if s.cfg.CloudflaredTunnelName != "" {
			initialURL = strings.TrimRight(s.cfg.TunnelPublicURL, "/")
		}
		providers = append(providers, tunnelProvider{name: "cloudflared", initialURL: initialURL, startupTimeout: 45 * time.Second,
			urlPattern: regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`),
			command: func(ctx context.Context) *exec.Cmd {
				if s.cfg.CloudflaredTunnelName != "" {
					return exec.CommandContext(ctx, exe, "tunnel", "--no-autoupdate", "run", s.cfg.CloudflaredTunnelName)
				}
				return exec.CommandContext(ctx, exe, "tunnel", "--no-autoupdate", "--url", origin)
			}})
	}
	if ssh, err := exec.LookPath("ssh"); err == nil && s.cfg.CloudflaredTunnelName == "" {
		interval := max(1, int((config.Duration(s.cfg.TunnelHeartbeatInterval)+time.Second-1)/time.Second))
		providers = append(providers, tunnelProvider{name: "pinggy", startupTimeout: 30 * time.Second,
			urlPattern: regexp.MustCompile(`https://[a-zA-Z0-9-]+\.(a\.)?pinggy\.(link|io|xyz)`),
			command: func(ctx context.Context) *exec.Cmd {
				return exec.CommandContext(ctx, ssh, "-T", "-p", "443",
					"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
					"-o", "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=10",
					"-o", fmt.Sprintf("ServerAliveInterval=%d", interval),
					"-o", "ServerAliveCountMax=3", "-o", "TCPKeepAlive=yes",
					"-R", "0:"+net.JoinHostPort(host, fmt.Sprint(s.cfg.Port)), "a.pinggy.io")
			}})
	}
	return providers
}

func reconnectDelay(base time.Duration, failures int, jitter float64) time.Duration {
	delay := base
	for i := 1; i < failures && delay < time.Minute; i++ {
		delay = min(time.Minute, delay*2)
	}
	// Equal jitter [50%,100%] prevents synchronized reconnect storms, with a hard cap.
	return time.Duration(float64(min(delay, time.Minute)) * (0.5 + 0.5*jitter))
}

func waitTunnelRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) superviseTunnels(ctx context.Context) {
	providers := s.tunnelProviders()
	if len(providers) == 0 {
		log.Warn().Msg("tunnel_unavailable: install cloudflared or ssh")
		return
	}
	// An independent supervisor; no bash queue, slots or locks are involved.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 1,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	s.superviseProviders(ctx, providers, func(ctx context.Context, url string) error { return probeTunnel(ctx, client, url) })
}

func (s *Server) superviseProviders(ctx context.Context, providers []tunnelProvider, probe func(context.Context, string) error) {
	if len(providers) == 0 {
		return
	}
	index, failures, providerFailures := 0, 0, 0
	for ctx.Err() == nil {
		provider := providers[index]
		start := time.Now()
		ready, err := runTunnel(ctx, provider, config.Duration(s.cfg.TunnelHeartbeatInterval),
			probe,
			func(url string) {
				log.Info().Str("provider", provider.name).Str("url", url).Msg("tunnel_connected")
				printTunnelURL(url)
			})
		if ctx.Err() != nil {
			log.Info().Str("provider", provider.name).Msg("tunnel_stopped")
			return
		}
		if ready && time.Since(start) >= 2*time.Minute {
			failures = 0
		}
		failures++
		if !ready {
			providerFailures++
			if providerFailures >= 2 {
				index = (index + 1) % len(providers)
				providerFailures = 0
			}
		} else {
			providerFailures = 0
		}
		delay := reconnectDelay(config.Duration(s.cfg.TunnelReconnectBackoff), failures, rand.Float64())
		log.Warn().Str("provider", provider.name).Err(err).Int("failures", failures).
			Dur("retry_in", delay).Msg("tunnel_disconnected_reconnecting")
		if !waitTunnelRetry(ctx, delay) {
			return
		}
	}
}

func runTunnel(parent context.Context, provider tunnelProvider, interval time.Duration,
	probe func(context.Context, string) error, onURL func(string)) (ready bool, cause error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	output := &tunnelOutput{pattern: provider.urlPattern, urls: make(chan string, 1)}
	cmd := provider.command(ctx)
	// Assigning writers lets os/exec own the pipe-draining goroutines and wait for them.
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start %s: %w", provider.name, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	waited := false
	defer func() {
		cancel()
		if !waited {
			<-exited
		}
	}()
	startup := time.NewTimer(provider.startupTimeout)
	defer startup.Stop()
	startupC := startup.C
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	publicURL := provider.initialURL
	if publicURL != "" {
		ready = true
		startup.Stop()
		startupC = nil
		onURL(publicURL)
	}
	missed := 0
	for {
		select {
		case <-parent.Done():
			return ready, parent.Err()
		case err := <-exited:
			waited = true
			if err == nil {
				err = errors.New("tunnel process exited normally but unexpectedly")
			}
			// Tail is bounded and kept at DEBUG: provider output may contain sensitive URLs.
			log.Debug().Str("provider", provider.name).Str("tail", output.tail()).Msg("tunnel_process_tail")
			return ready, fmt.Errorf("process exit: %w", err)
		case <-startupC:
			return false, errors.New("tunnel URL discovery timeout")
		case url := <-output.urls:
			if publicURL == "" {
				publicURL = url
				ready = true
				startup.Stop()
				startupC = nil
				onURL(url)
			}
		case <-ticker.C:
			if publicURL == "" {
				continue
			}
			probeCtx, stopProbe := context.WithTimeout(ctx, 5*time.Second)
			err := probe(probeCtx, publicURL)
			stopProbe()
			if err == nil {
				if missed > 0 {
					log.Info().Str("provider", provider.name).Msg("tunnel_health_restored")
				}
				missed = 0
				continue
			}
			missed++
			log.Warn().Str("provider", provider.name).Int("missed", missed).Err(err).Msg("tunnel_healthcheck_failed")
			if missed >= 3 {
				return ready, fmt.Errorf("three consecutive tunnel health checks failed: %w", err)
			}
		}
	}
}

func probeTunnel(ctx context.Context, client *http.Client, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz HTTP %d", resp.StatusCode)
	}
	var health struct {
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health); err != nil {
		return err
	}
	if !health.OK || health.Name != "devspace-go" {
		return errors.New("unexpected healthz response")
	}
	return nil
}
