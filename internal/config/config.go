// Package config loads, defaults, and validates the gateway's declarative YAML
// configuration. Loading fails fast: a malformed file, an unknown field, a
// missing referenced secret, or a rule naming an unknown downstream or client
// is an error at startup rather than a surprise at request time (design §7, §8).
package config

// Config is the gateway configuration.
type Config struct {
	// Listen is the host:port the client-facing server binds.
	Listen string `yaml:"listen"`
	// AllowedOrigins is the Origin allowlist for the DNS-rebinding guard.
	AllowedOrigins []string `yaml:"allowed_origins"`
	// Clients are the authenticated client identities.
	Clients []Client `yaml:"clients"`
	// Downstreams are the MCP servers the gateway proxies.
	Downstreams []Downstream `yaml:"downstreams"`
	// Policy is the deny-by-default access policy.
	Policy Policy `yaml:"policy"`
	// Redaction configures outbound secret and PII redaction.
	Redaction Redaction `yaml:"redaction"`
	// Audit configures the audit write path.
	Audit Audit `yaml:"audit"`
}

// Client maps a named identity to the environment variable holding its API key.
type Client struct {
	ID        string `yaml:"id"`
	APIKeyEnv string `yaml:"api_key_env"`
}

// Transport is a downstream transport kind.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// SessionMode controls how stdio subprocesses are shared across client sessions.
type SessionMode string

const (
	SessionPerClient SessionMode = "per_client"
	SessionShared    SessionMode = "shared"
)

// Downstream is one downstream MCP server.
type Downstream struct {
	Name string `yaml:"name"`
	// Transport is "stdio" or "http".
	Transport Transport `yaml:"transport"`
	// Command is the stdio subprocess argv (stdio transport only).
	Command []string `yaml:"command"`
	// URL is the remote endpoint (http transport only).
	URL string `yaml:"url"`
	// SessionMode applies to stdio downstreams; it defaults to per_client.
	SessionMode SessionMode `yaml:"session_mode"`
	// Pool bounds the stdio subprocess pool (stdio transport only).
	Pool Pool `yaml:"pool"`
	// Secrets are connection-level credentials injected by the gateway, keyed by
	// env var (stdio) or header name (http), each resolved from an env var.
	Secrets map[string]SecretRef `yaml:"secrets"`
	// Health configures the downstream health check.
	Health Health `yaml:"health"`
}

// Pool bounds an stdio subprocess pool.
type Pool struct {
	Max            int      `yaml:"max"`
	AcquireTimeout Duration `yaml:"acquire_timeout"`
	IdleTTL        Duration `yaml:"idle_ttl"`
}

// SecretRef names the environment variable holding a secret value.
type SecretRef struct {
	Env string `yaml:"env"`
}

// Health configures a downstream health check.
type Health struct {
	Interval Duration `yaml:"interval"`
}

// PolicyDefault is the default access decision when no rule matches.
type PolicyDefault string

const (
	DefaultDeny  PolicyDefault = "deny"
	DefaultAllow PolicyDefault = "allow"
)

// Policy is the access policy.
type Policy struct {
	Default PolicyDefault `yaml:"default"`
	Rules   []Rule        `yaml:"rules"`
}

// Rule allows a client to call a set of namespaced tools (wildcards permitted).
type Rule struct {
	Client string   `yaml:"client"`
	Allow  []string `yaml:"allow"`
}

// Redaction configures outbound redaction.
type Redaction struct {
	Patterns []Pattern `yaml:"patterns"`
	// MaxResultBytes caps a buffered tool result before scanning.
	MaxResultBytes int `yaml:"max_result_bytes"`
}

// Pattern is a literal-gated PII regex: the regex runs only when Anchor is
// present in the scanned text.
type Pattern struct {
	Name   string `yaml:"name"`
	Regex  string `yaml:"regex"`
	Anchor string `yaml:"anchor"`
}

// OverflowPolicy is the audit buffer's policy for success records under load.
type OverflowPolicy string

const (
	OverflowShed  OverflowPolicy = "shed"
	OverflowBlock OverflowPolicy = "block"
)

// Audit configures the async audit write path.
type Audit struct {
	// Sink is "stdout" or "file:/path/to/audit.jsonl".
	Sink string `yaml:"sink"`
	// Buffer is the bounded handoff buffer size.
	Buffer int `yaml:"buffer"`
	// HighWaterMark is the buffer fraction above which success records shed.
	HighWaterMark float64 `yaml:"high_water_mark"`
	// Flush controls batch flushing to the OS page cache.
	Flush Flush `yaml:"flush"`
	// FsyncInterval is the group-commit interval to disk.
	FsyncInterval Duration `yaml:"fsync_interval"`
	// Overflow is the success-record overflow policy: "shed" or "block".
	Overflow OverflowPolicy `yaml:"overflow"`
	// SecurityBlockTimeout bounds how long a security record blocks before the
	// request is failed closed.
	SecurityBlockTimeout Duration `yaml:"security_block_timeout"`
}

// Flush controls audit batch flushing.
type Flush struct {
	MaxRecords  int      `yaml:"max_records"`
	MaxInterval Duration `yaml:"max_interval"`
}
