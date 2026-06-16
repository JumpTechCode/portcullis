// Package aggregate presents many downstream servers as one tool catalog.
//
// It fans out the cursor-paginated tools/list across a client's allowed
// downstreams, namespaces tool names to keep them collision-safe, merges and
// re-paginates the result, and routes a namespaced call back to the downstream
// session that owns it.
package aggregate
