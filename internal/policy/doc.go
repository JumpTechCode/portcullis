// Package policy decides whether a client identity may call a tool.
//
// It evaluates declarative, deny-by-default allow rules for an
// (identity, tool) pair and produces the per-identity catalog filter. Catalog
// filtering is security-load-bearing: clients never see tools, schemas, or
// descriptions they are not permitted to call.
package policy
