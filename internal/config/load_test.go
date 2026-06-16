package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/config"
)

// setValidEnv sets every environment variable the valid testdata config
// references, so load passes the missing-secret validation.
func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PORTCULLIS_KEY_CLAUDE", "key-claude")
	t.Setenv("PORTCULLIS_KEY_CI", "key-ci")
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("SEARCH_BEARER", "bearer-token")
}

func TestLoadValidConfig(t *testing.T) {
	setValidEnv(t)

	c, err := config.Load(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q, want 127.0.0.1:8080", c.Listen)
	}
	if len(c.Clients) != 2 || c.Clients[0].ID != "claude-desktop" {
		t.Errorf("Clients = %+v, want claude-desktop first of two", c.Clients)
	}
	if len(c.Downstreams) != 2 {
		t.Fatalf("Downstreams = %d, want 2", len(c.Downstreams))
	}

	gh := c.Downstreams[0]
	if gh.Transport != config.TransportStdio {
		t.Errorf("github transport = %q, want stdio", gh.Transport)
	}
	if gh.SessionMode != config.SessionPerClient {
		t.Errorf("omitted session_mode = %q, want default per_client", gh.SessionMode)
	}
	if gh.Pool.AcquireTimeout.Duration() != 2*time.Second {
		t.Errorf("pool.acquire_timeout = %v, want 2s", gh.Pool.AcquireTimeout.Duration())
	}
	if gh.Pool.IdleTTL.Duration() != 90*time.Second {
		t.Errorf("pool.idle_ttl = %v, want 90s", gh.Pool.IdleTTL.Duration())
	}

	if c.Policy.Default != config.DefaultDeny {
		t.Errorf("policy.default = %q, want deny", c.Policy.Default)
	}
	if c.Redaction.MaxResultBytes != 1048576 {
		t.Errorf("max_result_bytes = %d, want 1048576", c.Redaction.MaxResultBytes)
	}
	if c.Audit.HighWaterMark != 0.85 {
		t.Errorf("high_water_mark = %v, want 0.85", c.Audit.HighWaterMark)
	}
	if c.Audit.Overflow != config.OverflowShed {
		t.Errorf("overflow = %q, want shed", c.Audit.Overflow)
	}
	if c.Audit.Flush.MaxInterval.Duration() != 50*time.Millisecond {
		t.Errorf("flush.max_interval = %v, want 50ms", c.Audit.Flush.MaxInterval.Duration())
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("PORTCULLIS_KEY_SOLO", "key-solo")
	t.Setenv("GH_TOKEN", "gh-token")

	c, err := config.Load(filepath.Join("testdata", "minimal.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	gh := c.Downstreams[0]
	if gh.SessionMode != config.SessionPerClient {
		t.Errorf("session_mode = %q, want default per_client", gh.SessionMode)
	}
	if gh.Pool.AcquireTimeout.Duration() != 2*time.Second {
		t.Errorf("acquire_timeout = %v, want default 2s", gh.Pool.AcquireTimeout.Duration())
	}
	if gh.Pool.IdleTTL.Duration() != 90*time.Second {
		t.Errorf("idle_ttl = %v, want default 90s", gh.Pool.IdleTTL.Duration())
	}
	if gh.Health.Interval.Duration() != 30*time.Second {
		t.Errorf("health.interval = %v, want default 30s", gh.Health.Interval.Duration())
	}

	a := c.Audit
	if a.Buffer != 10000 {
		t.Errorf("buffer = %d, want default 10000", a.Buffer)
	}
	if a.HighWaterMark != 0.85 {
		t.Errorf("high_water_mark = %v, want default 0.85", a.HighWaterMark)
	}
	if a.Overflow != config.OverflowShed {
		t.Errorf("overflow = %q, want default shed", a.Overflow)
	}
	if a.Flush.MaxRecords != 100 || a.Flush.MaxInterval.Duration() != 50*time.Millisecond {
		t.Errorf("flush = %+v, want default {100 50ms}", a.Flush)
	}
	if a.FsyncInterval.Duration() != time.Second {
		t.Errorf("fsync_interval = %v, want default 1s", a.FsyncInterval.Duration())
	}
	if a.SecurityBlockTimeout.Duration() != 250*time.Millisecond {
		t.Errorf("security_block_timeout = %v, want default 250ms", a.SecurityBlockTimeout.Duration())
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Setenv("PORTCULLIS_KEY_SOLO", "key-solo")
	if _, err := config.Load(filepath.Join("testdata", "invalid.yaml")); err == nil {
		t.Error("Load of a parseable-but-invalid config returned nil error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := config.Load(filepath.Join("testdata", "does-not-exist.yaml")); err == nil {
		t.Error("Load of a missing file returned nil error")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	setValidEnv(t)
	if _, err := config.Load(filepath.Join("testdata", "unknown-field.yaml")); err == nil {
		t.Error("Load of a config with an unknown field returned nil error")
	}
}
