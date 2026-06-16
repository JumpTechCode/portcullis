// Package redact performs outbound redaction of sensitive values from byte
// payloads (tool results, JSON-RPC errors, and notifications) before they leave
// the gateway or are written to the audit log.
//
// It removes two classes of data:
//
//   - Injected secret values, matched by exact value. All secrets are matched in
//     a single pass with an Aho-Corasick automaton, so cost is independent of the
//     number of secrets rather than O(secrets) repeated scans.
//   - PII, matched by a literal-gated regular expression. Each pattern's compiled
//     RE2 regexp is consulted only when a cheap byte-substring pre-screen for its
//     anchor literal succeeds.
//
// A Redactor is built once with New and is immutable thereafter: it holds only
// read-only state, so Redact is safe for concurrent use by multiple goroutines.
// Redact never mutates its input; it returns a freshly allocated slice. Callers
// therefore treat tool results and call arguments as immutable.
//
// Overlap policy: secret occurrences are merged into maximal spans. Overlapping
// or directly adjacent secret matches (for example secrets "abc" and "bcd" in
// "abcd", or a secret that is a substring of another) collapse into one redacted
// span replaced by a single placeholder, so the output is never corrupted and no
// byte is redacted twice.
//
// Out of scope (future work): encoding canonicalization. This package matches
// only the literal bytes it is given; it does not decode base64, percent-encoding,
// or other transforms before matching. A secret that appears only in an encoded
// form is not redacted by this implementation.
package redact

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
)

// Placeholder is the text substituted for every redacted secret value and every
// literal-gated PII match.
const Placeholder = "[REDACTED]"

// Pattern is a literal-gated PII pattern. The application layer maps configured
// patterns onto this type.
//
// Regex is an RE2 regular expression (see the regexp package). Anchor is a plain
// literal substring that must be present in the input for the regex to be run at
// all; it is a constant-factor pre-screen, not a correctness requirement, so it
// should be a literal that necessarily appears in every match (often a fixed
// prefix or separator of the pattern). Name identifies the pattern for operators
// and is not used during matching.
type Pattern struct {
	Name   string
	Regex  string
	Anchor string
}

// compiledPattern is the internal, ready-to-run form of a Pattern.
type compiledPattern struct {
	anchor []byte
	re     *regexp.Regexp
}

// Redactor removes secret values and PII from byte payloads. It is immutable
// after construction and safe for concurrent use.
type Redactor struct {
	ac       *ahoCorasick
	patterns []compiledPattern
}

// New builds a Redactor over the given secret values and PII patterns.
//
// Secrets are loaded into a single Aho-Corasick automaton for single-pass,
// multi-string exact matching; empty secret strings are ignored. Each pattern's
// regular expression is compiled with regexp.Compile, and the first uncompilable
// regex makes New return a non-nil error. The returned Redactor is immutable and
// safe for concurrent use.
func New(secrets []string, patterns []Pattern) (*Redactor, error) {
	ac := newAhoCorasick(secrets)

	compiled := make([]compiledPattern, 0, len(patterns))
	for i := range patterns {
		p := patterns[i]
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, fmt.Errorf("redact: compiling pattern %q: %w", p.Name, err)
		}
		compiled = append(compiled, compiledPattern{
			anchor: []byte(p.Anchor),
			re:     re,
		})
	}

	return &Redactor{ac: ac, patterns: compiled}, nil
}

// Redact returns a new slice in which every occurrence of a registered secret
// value and every literal-gated PII match has been replaced by Placeholder. The
// returned count is the total number of replacements: one per merged secret span
// (see the package overlap policy) plus one per PII match. The input slice is
// never modified.
func (r *Redactor) Redact(data []byte) (redacted []byte, count int) {
	// Stage 1: exact secret matching in a single Aho-Corasick pass, replacing
	// each merged span with the placeholder.
	out, secretCount := r.redactSecrets(data)

	// Stage 2: literal-gated PII patterns. These run over the secret-redacted
	// output so a pattern can never re-expose bytes already removed as secrets.
	piiCount := 0
	for i := range r.patterns {
		p := r.patterns[i]
		// The anchor pre-screen is a constant-factor optimization: RE2 matching
		// is linear in the input, but bytes.Contains is far cheaper, so skipping
		// patterns whose anchor is absent avoids the per-pattern scan entirely.
		// An empty anchor never short-circuits (bytes.Contains(_, nil) is true).
		if len(p.anchor) != 0 && !bytes.Contains(out, p.anchor) {
			continue
		}
		matches := p.re.FindAllIndex(out, -1)
		if len(matches) == 0 {
			continue
		}
		spans := make([]span, len(matches))
		for j, m := range matches {
			spans[j] = span{start: m[0], end: m[1]}
		}
		out = replaceSpans(out, spans)
		piiCount += len(matches)
	}

	return out, secretCount + piiCount
}

// redactSecrets runs the Aho-Corasick automaton over data, merges overlapping
// and adjacent match spans, and replaces each merged span with the placeholder.
// It always returns a newly allocated slice, so the caller's input is untouched
// even when there are no matches.
func (r *Redactor) redactSecrets(data []byte) (out []byte, count int) {
	spans := r.ac.findSpans(data)
	if len(spans) == 0 {
		return append([]byte(nil), data...), 0
	}
	merged := mergeSpans(spans)
	return replaceSpans(data, merged), len(merged)
}

// span is a half-open byte range [start, end) within an input slice.
type span struct {
	start, end int
}

// mergeSpans collapses overlapping or directly adjacent spans into maximal
// spans. It sorts by start first so the result is correct regardless of the
// order in which matches were discovered, then sweeps once tracking a running
// maximum end. Adjacent spans (prev.end == next.start) are merged so two
// back-to-back secrets become a single redacted run rather than two placeholders.
func mergeSpans(spans []span) []span {
	slices.SortFunc(spans, func(a, b span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})

	merged := make([]span, 0, len(spans))
	cur := spans[0]
	for _, s := range spans[1:] {
		if s.start <= cur.end {
			if s.end > cur.end {
				cur.end = s.end
			}
			continue
		}
		merged = append(merged, cur)
		cur = s
	}
	merged = append(merged, cur)
	return merged
}

// replaceSpans returns a new slice in which each span in spans is replaced by
// Placeholder. spans must be non-empty, sorted by start, and non-overlapping
// (mergeSpans guarantees this for secrets; regexp.FindAllIndex guarantees it for
// PII). The input slice is never modified.
func replaceSpans(data []byte, spans []span) []byte {
	placeholder := []byte(Placeholder)

	// Pre-size the output exactly to avoid reallocations on the hot path.
	size := len(data)
	for _, s := range spans {
		size += len(placeholder) - (s.end - s.start)
	}

	out := make([]byte, 0, size)
	last := 0
	for _, s := range spans {
		out = append(out, data[last:s.start]...)
		out = append(out, placeholder...)
		last = s.end
	}
	out = append(out, data[last:]...)
	return out
}
