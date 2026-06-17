package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Validate checks the configuration and returns a combined error describing
// every problem found, or nil if the configuration is sound. It is the
// fail-fast gate: a missing referenced secret, an unknown transport or
// session_mode, a rule naming an unknown client or downstream, an
// uncompilable redaction pattern, or an out-of-range tuning value is reported
// here rather than at request time (design §7, §8).
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.Listen == "" {
		add("listen must not be empty")
	}

	clientIDs := c.validateClients(add)
	downstreamNames := c.validateDownstreams(add)
	c.validatePolicy(add, clientIDs, downstreamNames)
	c.validateRedaction(add)
	c.validateAudit(add)

	return errors.Join(errs...)
}

func (c *Config) validateClients(add func(string, ...any)) map[string]bool {
	if len(c.Clients) == 0 {
		add("at least one client must be configured")
	}
	ids := make(map[string]bool, len(c.Clients))
	for i, cl := range c.Clients {
		switch {
		case cl.ID == "":
			add("clients[%d]: id must not be empty", i)
		case ids[cl.ID]:
			add("clients[%d]: duplicate client id %q", i, cl.ID)
		default:
			ids[cl.ID] = true
		}
		switch {
		case cl.APIKeyEnv == "":
			add("client %q: api_key_env must not be empty", cl.ID)
		case os.Getenv(cl.APIKeyEnv) == "":
			// Identify the client by its id rather than echoing the env var name:
			// the name is an internal detail and must not flow into logs (the
			// reload path logs validation errors).
			add("client %q: api key environment variable is not set", cl.ID)
		}
	}
	return ids
}

func (c *Config) validateDownstreams(add func(string, ...any)) map[string]bool {
	if len(c.Downstreams) == 0 {
		add("at least one downstream must be configured")
	}
	names := make(map[string]bool, len(c.Downstreams))
	for i := range c.Downstreams {
		d := c.Downstreams[i]
		switch {
		case d.Name == "":
			add("downstreams[%d]: name must not be empty", i)
		case strings.Contains(d.Name, domain.NamespaceSeparator):
			add("downstream %q: name must not contain the namespace separator %q", d.Name, domain.NamespaceSeparator)
		case names[d.Name]:
			add("downstream %q: duplicate name", d.Name)
		default:
			names[d.Name] = true
		}

		switch d.Transport {
		case TransportStdio:
			if len(d.Command) == 0 {
				add("downstream %q: stdio transport requires a command", d.Name)
			}
			if d.SessionMode != SessionPerClient && d.SessionMode != SessionShared {
				add("downstream %q: invalid session_mode %q (want per_client or shared)", d.Name, d.SessionMode)
			}
			if d.Pool.Max <= 0 {
				add("downstream %q: pool.max must be positive", d.Name)
			}
			if d.Pool.AcquireTimeout <= 0 {
				add("downstream %q: pool.acquire_timeout must be positive", d.Name)
			}
			if d.Pool.IdleTTL <= 0 {
				add("downstream %q: pool.idle_ttl must be positive", d.Name)
			}
		case TransportHTTP:
			if d.URL == "" {
				add("downstream %q: http transport requires a url", d.Name)
			}
			if len(d.Command) != 0 {
				add("downstream %q: http transport must not set a command", d.Name)
			}
			if d.SessionMode != "" {
				add("downstream %q: http transport must not set session_mode (it applies to stdio)", d.Name)
			}
			if d.Pool != (Pool{}) {
				add("downstream %q: http transport must not set a pool (it applies to stdio)", d.Name)
			}
		default:
			add("downstream %q: invalid transport %q (want stdio or http)", d.Name, d.Transport)
		}

		for key, ref := range d.Secrets {
			switch {
			case ref.Env == "":
				add("downstream %q: secret %q: env must not be empty", d.Name, key)
			case os.Getenv(ref.Env) == "":
				add("downstream %q: secret %q: env var %q is not set", d.Name, key, ref.Env)
			}
		}
	}
	return names
}

func (c *Config) validatePolicy(add func(string, ...any), clientIDs, downstreamNames map[string]bool) {
	if c.Policy.Default != DefaultDeny && c.Policy.Default != DefaultAllow {
		add("policy.default must be deny or allow, got %q", c.Policy.Default)
	}
	for i, r := range c.Policy.Rules {
		if !clientIDs[r.Client] {
			add("policy.rules[%d]: unknown client %q", i, r.Client)
		}
		for _, entry := range r.Allow {
			ds, ok := allowEntryDownstream(entry)
			switch {
			case !ok:
				add("policy.rules[%d]: malformed allow entry %q (want downstream__tool or downstream__*)", i, entry)
			case !downstreamNames[ds]:
				add("policy.rules[%d]: allow entry %q names unknown downstream %q", i, entry, ds)
			}
		}
	}
}

func (c *Config) validateRedaction(add func(string, ...any)) {
	for i, p := range c.Redaction.Patterns {
		if p.Name == "" {
			add("redaction.patterns[%d]: name must not be empty", i)
		}
		if p.Anchor == "" {
			add("redaction.patterns[%d] (%s): anchor must not be empty (the regex is literal-gated)", i, p.Name)
		}
		if _, err := regexp.Compile(p.Regex); err != nil {
			add("redaction.patterns[%d] (%s): invalid regex: %v", i, p.Name, err)
		}
	}
	if c.Redaction.MaxResultBytes <= 0 {
		add("redaction.max_result_bytes must be positive")
	}
}

func (c *Config) validateAudit(add func(string, ...any)) {
	if !validSink(c.Audit.Sink) {
		add("audit.sink must be stdout or file:<path>, got %q", c.Audit.Sink)
	}
	if c.Audit.Buffer <= 0 {
		add("audit.buffer must be positive")
	}
	if c.Audit.HighWaterMark <= 0 || c.Audit.HighWaterMark > 1 {
		add("audit.high_water_mark must be in (0, 1], got %v", c.Audit.HighWaterMark)
	}
	if c.Audit.Overflow != OverflowShed && c.Audit.Overflow != OverflowBlock {
		add("audit.overflow must be shed or block, got %q", c.Audit.Overflow)
	}
	if c.Audit.Flush.MaxRecords <= 0 {
		add("audit.flush.max_records must be positive")
	}
	if c.Audit.Flush.MaxInterval <= 0 {
		add("audit.flush.max_interval must be positive")
	}
	if c.Audit.FsyncInterval <= 0 {
		add("audit.fsync_interval must be positive")
	}
	if c.Audit.SecurityBlockTimeout <= 0 {
		add("audit.security_block_timeout must be positive")
	}
}

// allowEntryDownstream returns the downstream named by a policy allow entry —
// either "downstream__tool" or the wildcard "downstream__*" — and whether the
// entry is well-formed.
func allowEntryDownstream(entry string) (string, bool) {
	if wildcard := domain.NamespaceSeparator + "*"; strings.HasSuffix(entry, wildcard) {
		ds := strings.TrimSuffix(entry, wildcard)
		if ds == "" || strings.Contains(ds, domain.NamespaceSeparator) {
			return "", false
		}
		return ds, true
	}
	ref, err := domain.ParseToolRef(entry)
	if err != nil {
		return "", false
	}
	return ref.Downstream, true
}

// validSink reports whether sink is a supported audit sink: stdout or a
// non-empty file path.
func validSink(sink string) bool {
	const filePrefix = "file:"
	return sink == "stdout" || (strings.HasPrefix(sink, filePrefix) && len(sink) > len(filePrefix))
}
