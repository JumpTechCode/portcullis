// Package config loads and validates the declarative YAML configuration.
//
// Validation is fail-fast: bad input, a missing secret, or a rule that names an
// unknown downstream prevents startup. The client/auth and policy maps support
// scoped hot-reload via an atomic in-memory swap; downstream topology changes
// remain restart-required.
package config
