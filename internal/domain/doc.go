// Package domain holds the shared value types and port interfaces that cross
// Portcullis package boundaries.
//
// It is the leaf of the dependency graph: it imports only the standard library
// (and protocol SDK types) and nothing from other internal packages. Concrete
// packages depend on domain, never the other way around, which keeps the
// internal import graph acyclic by construction.
package domain
