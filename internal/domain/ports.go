package domain

// This file declares the port interfaces that cross Portcullis package
// boundaries. Concrete packages satisfy them structurally and never import their
// consumers, which keeps the internal import graph acyclic. Cross-boundary data
// travels as the value types in this package; cross-boundary behavior travels as
// these interfaces, injected at the composition root.

// Decider evaluates whether a client may call a tool.
//
// Implementations are deny-by-default and resolve conflicting rules
// most-restrictively: a deny beats an allow (design §7). The evaluation is a
// pure in-memory lookup, so it takes no context and never fails — the outcome is
// always a Decision carrying a reason code.
type Decider interface {
	Decide(client Identity, tool ToolRef) Decision
}

// CatalogFilter reduces a full namespaced catalog to the subset a client is
// permitted to see.
//
// Filtering is security-load-bearing: a client must never see the schema or
// description of a tool it may not call (design §8).
type CatalogFilter interface {
	Filter(client Identity, full Catalog) Catalog
}
