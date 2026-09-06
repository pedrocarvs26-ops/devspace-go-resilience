package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Limits are per job, including descendants, not an aggregate host budget.
// Hard limits are opt-in: they require OS facilities unavailable on some hosts.
type BashResourceLimit struct {
	Enabled                 bool   `json:"enabled"`
	CPUPercent              int    `json:"cpuPercent"` // percentage of ALL logical CPUs
	MemoryMB                int64  `json:"memoryMB"`
	BandwidthBytesPerSecond int64  `json:"bandwidthBytesPerSecond"` // Windows outbound; Linux dedicated namespace
	Nice                    int    `json:"nice"`
	SystemdUser             bool   `json:"systemdUser"`
	NetworkNamespacePath    string `json:"networkNamespacePath,omitempty"`
	NetworkInterface        string `json:"networkInterface,omitempty"` // host veth shaped by administrator
}

type BashJobs struct {
	MaxConcurrent int    `json:"maxConcurrent"`
	MaxRetained   int    `json:"maxRetained"`
	OutputBytes   int    `json:"outputBytes"`
	Timeout       string `json:"timeout"`
	MaxTimeout    string `json:"maxTimeout"`
	Retention     string `json:"retention"`
}

func DefaultBashResourceLimit() BashResourceLimit {
	return BashResourceLimit{CPUPercent: 25, MemoryMB: 1024, Nice: 10, SystemdUser: true}
}
func DefaultBashJobs() BashJobs {
	return BashJobs{MaxConcurrent: 2, MaxRetained: 64, OutputBytes: 1024 * 1024,
		Timeout: "1h", MaxTimeout: "24h", Retention: "30m"}
}

func mergeRuntimeConfig(c *Config, raw map[string]json.RawMessage) {
	// Decode nested objects OVER defaults, preserving explicitly specified zero/false.
	fields := map[string]any{
		"cloudflaredTunnelName": &c.CloudflaredTunnelName, "tunnelPublicUrl": &c.TunnelPublicURL,
		"httpReadTimeout": &c.HTTPReadTimeout, "httpWriteTimeout": &c.HTTPWriteTimeout,
		"httpIdleTimeout": &c.HTTPIdleTimeout, "tunnelHeartbeatInterval": &c.TunnelHeartbeatInterval,
		"tunnelReconnectBackoff": &c.TunnelReconnectBackoff,
		"bashResourceLimit":      &c.BashResourceLimit, "bashJobs": &c.BashJobs,
	}
	for k, dst := range fields {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
}

// Duration is used only after ValidateRuntime has succeeded.
func Duration(value string) time.Duration { d, _ := time.ParseDuration(value); return d }

func (c *Config) ValidateRuntime() error {
	if c.loadError != nil {
		return c.loadError
	}
	if c.CloudflaredTunnelName != "" {
		u, err := url.Parse(c.TunnelPublicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("a named Cloudflare tunnel requires tunnelPublicUrl=https://YOUR-DOMAIN (no path, credentials, query or fragment)")
		}
		if strings.HasPrefix(c.CloudflaredTunnelName, "-") {
			return fmt.Errorf("invalid cloudflaredTunnelName")
		}
	}

	for name, value := range map[string]string{
		"httpReadTimeout": c.HTTPReadTimeout, "httpWriteTimeout": c.HTTPWriteTimeout,
		"httpIdleTimeout": c.HTTPIdleTimeout, "tunnelHeartbeatInterval": c.TunnelHeartbeatInterval,
		"tunnelReconnectBackoff": c.TunnelReconnectBackoff, "bashJobs.timeout": c.BashJobs.Timeout,
		"bashJobs.maxTimeout": c.BashJobs.MaxTimeout, "bashJobs.retention": c.BashJobs.Retention,
	} {
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 {
			return fmt.Errorf("%s must be a nonnegative duration (e.g. 15s)", name)
		}
	}
	if Duration(c.TunnelHeartbeatInterval) < time.Second || Duration(c.TunnelHeartbeatInterval) > time.Hour {
		return fmt.Errorf("tunnelHeartbeatInterval must be between 1s and 1h")
	}
	if Duration(c.TunnelReconnectBackoff) < 100*time.Millisecond || Duration(c.TunnelReconnectBackoff) > time.Minute {
		return fmt.Errorf("tunnelReconnectBackoff must be between 100ms and 1m")
	}
	j := c.BashJobs
	if j.MaxConcurrent < 1 || j.MaxConcurrent > 64 || j.MaxRetained < j.MaxConcurrent || j.MaxRetained > 1024 {
		return fmt.Errorf("bashJobs requires 1 <= maxConcurrent <= 64 and maxConcurrent <= maxRetained <= 1024")
	}
	if j.OutputBytes < 4096 || j.OutputBytes > 16*1024*1024 || int64(j.OutputBytes)*int64(j.MaxRetained) > 256*1024*1024 {
		return fmt.Errorf("bashJobs outputBytes must be 4KiB..16MiB; maxRetained * outputBytes must not exceed 256MiB")
	}
	if Duration(j.Timeout) < time.Second || Duration(j.Timeout) > Duration(j.MaxTimeout) || Duration(j.MaxTimeout) > 7*24*time.Hour || Duration(j.Retention) < time.Second || Duration(j.Retention) > 7*24*time.Hour {
		return fmt.Errorf("bashJobs requires 1s <= timeout <= maxTimeout <= 168h and 1s <= retention <= 168h")
	}
	r := c.BashResourceLimit
	if r.Nice < 0 || r.Nice > 19 || r.CPUPercent < 1 || r.CPUPercent > 100 || r.MemoryMB < 16 || r.MemoryMB > 1048576 || r.BandwidthBytesPerSecond < 0 {
		return fmt.Errorf("invalid bashResourceLimit: nice 0..19, cpuPercent 1..100, memoryMB 16..1048576, bandwidthBytesPerSecond >= 0 required")
	}
	return nil
}
