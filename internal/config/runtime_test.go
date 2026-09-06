package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeConfigDefaultsAndPartialMerge(t *testing.T) {
	for _, text := range []string{`{"lang":"en"}`, `{"bashJobs":{"maxConcurrent":1},"bashResourceLimit":{"enabled":false},"httpWriteTimeout":"0s"}`} {
		dir := t.TempDir()
		t.Setenv("DEVSPACE_CONFIG_DIR", dir)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		c := LoadConfig()
		if err := c.ValidateRuntime(); err != nil {
			t.Fatal(err)
		}
		if c.BashJobs.OutputBytes != 1024*1024 || c.BashJobs.MaxRetained != 64 || c.BashResourceLimit.Nice != 10 || c.BashResourceLimit.Enabled {
			t.Fatalf("defaults lost: %+v %+v", c.BashJobs, c.BashResourceLimit)
		}
	}
}

func TestRuntimeConfigValidation(t *testing.T) {
	tests := []func(*Config){
		func(c *Config) { c.HTTPReadTimeout = "-1s" },
		func(c *Config) { c.HTTPWriteTimeout = "wrong" },
		func(c *Config) { c.TunnelHeartbeatInterval = "0s" },
		func(c *Config) { c.TunnelReconnectBackoff = "0s" },
		func(c *Config) { c.BashJobs.MaxConcurrent = 0 },
		func(c *Config) { c.BashJobs.OutputBytes = 1 },
		func(c *Config) { c.BashJobs.Timeout = "999h" },
		func(c *Config) { c.BashResourceLimit.CPUPercent = 0 },
		func(c *Config) { c.BashResourceLimit.Nice = -20 },
	}
	for i, mutate := range tests {
		c := DefaultConfig()
		mutate(c)
		if c.ValidateRuntime() == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestMalformedRuntimeConfigDoesNotSilentlyDisableLimits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVSPACE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"bashResourceLimit":{"enabled":true,"cpuPercent":"bad"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if LoadConfig().ValidateRuntime() == nil {
		t.Fatal("malformed config silently fell back to unguarded defaults")
	}
}
