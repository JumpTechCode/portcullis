package config_test

import (
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/config"
)

// validConfig builds a fresh, valid configuration (already defaulted) in code,
// so each rejection case can mutate an independent copy. It references the same
// environment variables as setValidEnv.
func validConfig() *config.Config {
	return &config.Config{
		Listen:         "127.0.0.1:8080",
		AllowedOrigins: []string{"http://localhost"},
		Clients: []config.Client{
			{ID: "claude-desktop", APIKeyEnv: "PORTCULLIS_KEY_CLAUDE"},
			{ID: "ci-bot", APIKeyEnv: "PORTCULLIS_KEY_CI"},
		},
		Downstreams: []config.Downstream{
			{
				Name:        "github",
				Transport:   config.TransportStdio,
				Command:     []string{"npx", "-y", "@modelcontextprotocol/server-github"},
				SessionMode: config.SessionPerClient,
				Pool: config.Pool{
					Max:            16,
					AcquireTimeout: config.Duration(2 * time.Second),
					IdleTTL:        config.Duration(90 * time.Second),
				},
				Secrets: map[string]config.SecretRef{"GITHUB_TOKEN": {Env: "GH_TOKEN"}},
				Health:  config.Health{Interval: config.Duration(30 * time.Second)},
			},
			{
				Name:      "search",
				Transport: config.TransportHTTP,
				URL:       "https://mcp.example.com/mcp",
				Secrets:   map[string]config.SecretRef{"Authorization": {Env: "SEARCH_BEARER"}},
			},
		},
		Policy: config.Policy{
			Default: config.DefaultDeny,
			Rules: []config.Rule{
				{Client: "claude-desktop", Allow: []string{"github__create_issue", "search__*"}},
				{Client: "ci-bot", Allow: []string{"github__create_issue"}},
			},
		},
		Redaction: config.Redaction{
			Patterns:       []config.Pattern{{Name: "aws-key", Regex: `AKIA[0-9A-Z]{16}`, Anchor: "AKIA"}},
			MaxResultBytes: 1048576,
		},
		Audit: config.Audit{
			Sink:                 "stdout",
			Buffer:               10000,
			HighWaterMark:        0.85,
			Flush:                config.Flush{MaxRecords: 100, MaxInterval: config.Duration(50 * time.Millisecond)},
			FsyncInterval:        config.Duration(1 * time.Second),
			Overflow:             config.OverflowShed,
			SecurityBlockTimeout: config.Duration(250 * time.Millisecond),
		},
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	setValidEnv(t)
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	setValidEnv(t)

	const unsetEnv = "PORTCULLIS_DEFINITELY_UNSET_a1b2c3"

	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"empty listen", func(c *config.Config) { c.Listen = "" }},

		{"no clients", func(c *config.Config) { c.Clients = nil }},
		{"client empty id", func(c *config.Config) { c.Clients[0].ID = "" }},
		{"client empty api_key_env", func(c *config.Config) { c.Clients[0].APIKeyEnv = "" }},
		{"client env var unset", func(c *config.Config) { c.Clients[0].APIKeyEnv = unsetEnv }},
		{"duplicate client ids", func(c *config.Config) { c.Clients[1].ID = "claude-desktop" }},

		{"no downstreams", func(c *config.Config) { c.Downstreams = nil }},
		{"downstream empty name", func(c *config.Config) { c.Downstreams[0].Name = "" }},
		{"downstream name with separator", func(c *config.Config) { c.Downstreams[0].Name = "git__hub" }},
		{"duplicate downstream names", func(c *config.Config) { c.Downstreams[1].Name = "github" }},
		{"invalid transport", func(c *config.Config) { c.Downstreams[0].Transport = "carrier-pigeon" }},
		{"stdio without command", func(c *config.Config) { c.Downstreams[0].Command = nil }},
		{"http without url", func(c *config.Config) { c.Downstreams[1].URL = "" }},
		{"http with command", func(c *config.Config) { c.Downstreams[1].Command = []string{"oops"} }},
		{"http with session_mode", func(c *config.Config) { c.Downstreams[1].SessionMode = config.SessionShared }},
		{"http with pool", func(c *config.Config) { c.Downstreams[1].Pool = config.Pool{Max: 8} }},
		{"invalid session_mode", func(c *config.Config) { c.Downstreams[0].SessionMode = "sometimes" }},
		{"pool max not positive", func(c *config.Config) { c.Downstreams[0].Pool.Max = 0 }},
		{"pool acquire_timeout not positive", func(c *config.Config) { c.Downstreams[0].Pool.AcquireTimeout = 0 }},
		{"pool idle_ttl not positive", func(c *config.Config) { c.Downstreams[0].Pool.IdleTTL = 0 }},
		{"secret empty env", func(c *config.Config) { c.Downstreams[0].Secrets["GITHUB_TOKEN"] = config.SecretRef{Env: ""} }},
		{"secret env var unset", func(c *config.Config) { c.Downstreams[0].Secrets["GITHUB_TOKEN"] = config.SecretRef{Env: unsetEnv} }},

		{"invalid policy default", func(c *config.Config) { c.Policy.Default = "maybe" }},
		{"rule unknown client", func(c *config.Config) { c.Policy.Rules[0].Client = "ghost" }},
		{"rule unknown downstream", func(c *config.Config) { c.Policy.Rules[0].Allow = []string{"gitlab__create_issue"} }},
		{"rule malformed allow entry", func(c *config.Config) { c.Policy.Rules[0].Allow = []string{"no-separator"} }},
		{"rule wildcard empty downstream", func(c *config.Config) { c.Policy.Rules[0].Allow = []string{"__*"} }},

		{"pattern bad regex", func(c *config.Config) { c.Redaction.Patterns[0].Regex = "AKIA[" }},
		{"pattern empty anchor", func(c *config.Config) { c.Redaction.Patterns[0].Anchor = "" }},
		{"pattern empty name", func(c *config.Config) { c.Redaction.Patterns[0].Name = "" }},
		{"max_result_bytes not positive", func(c *config.Config) { c.Redaction.MaxResultBytes = 0 }},

		{"invalid audit sink", func(c *config.Config) { c.Audit.Sink = "carrier-pigeon" }},
		{"audit buffer not positive", func(c *config.Config) { c.Audit.Buffer = 0 }},
		{"high_water_mark too high", func(c *config.Config) { c.Audit.HighWaterMark = 1.5 }},
		{"high_water_mark not positive", func(c *config.Config) { c.Audit.HighWaterMark = 0 }},
		{"invalid overflow", func(c *config.Config) { c.Audit.Overflow = "drop-everything" }},
		{"flush max_records not positive", func(c *config.Config) { c.Audit.Flush.MaxRecords = 0 }},
		{"flush max_interval not positive", func(c *config.Config) { c.Audit.Flush.MaxInterval = 0 }},
		{"fsync_interval not positive", func(c *config.Config) { c.Audit.FsyncInterval = 0 }},
		{"security_block_timeout not positive", func(c *config.Config) { c.Audit.SecurityBlockTimeout = 0 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			if err := c.Validate(); err == nil {
				t.Errorf("Validate accepted an invalid config: %s", tc.name)
			}
		})
	}
}
