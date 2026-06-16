// Package app is the composition root.
//
// It is the only package that imports concrete implementations and wires them
// together: loading configuration, constructing the registry, assembling the
// per-call pipeline from its stages, and starting the edge server. Keeping the
// wiring here lets every other package depend only on domain interfaces.
package app
