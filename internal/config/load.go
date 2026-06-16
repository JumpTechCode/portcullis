package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Documented defaults (design §5, §8) applied when a field is omitted.
const (
	defaultAcquireTimeout       = 2 * time.Second
	defaultIdleTTL              = 90 * time.Second
	defaultHealthInterval       = 30 * time.Second
	defaultAuditBuffer          = 10000
	defaultHighWaterMark        = 0.85
	defaultSecurityBlockTimeout = 250 * time.Millisecond
	defaultFsyncInterval        = 1 * time.Second
	defaultFlushMaxRecords      = 100
	defaultFlushMaxInterval     = 50 * time.Millisecond
)

// Load reads, defaults, and validates the configuration at path. Any failure —
// unreadable file, malformed YAML, unknown field, or a validation error — is
// returned, so a bad configuration stops startup rather than surfacing later.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the config path is operator-supplied at startup, not attacker-controlled.
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	c, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return c, nil
}

// parse decodes YAML into a Config, rejecting unknown fields so a typo'd key is
// an error rather than a silently ignored setting.
func parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyDefaults fills omitted fields with their documented defaults.
func (c *Config) applyDefaults() {
	for i := range c.Downstreams {
		d := &c.Downstreams[i]
		if d.Transport == TransportStdio {
			if d.SessionMode == "" {
				d.SessionMode = SessionPerClient
			}
			if d.Pool.AcquireTimeout == 0 {
				d.Pool.AcquireTimeout = Duration(defaultAcquireTimeout)
			}
			if d.Pool.IdleTTL == 0 {
				d.Pool.IdleTTL = Duration(defaultIdleTTL)
			}
		}
		if d.Health.Interval == 0 {
			d.Health.Interval = Duration(defaultHealthInterval)
		}
	}

	a := &c.Audit
	if a.Buffer == 0 {
		a.Buffer = defaultAuditBuffer
	}
	if a.HighWaterMark == 0 {
		a.HighWaterMark = defaultHighWaterMark
	}
	if a.Overflow == "" {
		a.Overflow = OverflowShed
	}
	if a.SecurityBlockTimeout == 0 {
		a.SecurityBlockTimeout = Duration(defaultSecurityBlockTimeout)
	}
	if a.FsyncInterval == 0 {
		a.FsyncInterval = Duration(defaultFsyncInterval)
	}
	if a.Flush.MaxRecords == 0 {
		a.Flush.MaxRecords = defaultFlushMaxRecords
	}
	if a.Flush.MaxInterval == 0 {
		a.Flush.MaxInterval = Duration(defaultFlushMaxInterval)
	}
}
