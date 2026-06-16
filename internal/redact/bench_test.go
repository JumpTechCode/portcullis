package redact_test

import (
	"testing"

	"github.com/JumpTechCode/portcullis/internal/redact"
)

// benchPayloadSize is the ~1MB hot-path payload size used by the benchmarks
// (design §9), so the redaction throughput claim is measurable.
const benchPayloadSize = 1 << 20

// BenchmarkRedact measures the combined hot path: single-pass Aho-Corasick
// secret matching plus literal-gated PII regex over a ~1MB payload.
func BenchmarkRedact(b *testing.B) {
	r, err := redact.New(
		[]string{"sk-deadbeef", "alpha", "bravo", "charlie"},
		[]redact.Pattern{
			{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="},
			{Name: "bearer", Regex: `Bearer [A-Za-z0-9]+`, Anchor: "Bearer "},
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	data := largePayload(benchPayloadSize, "sk-deadbeef", "id=")

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Redact(data)
	}
}

// BenchmarkRedactByValueOnly isolates the Aho-Corasick secret-matching path
// (no PII patterns) so its cost is independently measurable.
func BenchmarkRedactByValueOnly(b *testing.B) {
	r, err := redact.New(
		[]string{"sk-deadbeef", "alpha", "bravo", "charlie"},
		nil,
	)
	if err != nil {
		b.Fatal(err)
	}
	data := largePayload(benchPayloadSize, "sk-deadbeef", "id=")

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Redact(data)
	}
}

// BenchmarkRedactGatedRegexOnly isolates the literal-gated PII path (no
// secrets) so the regex contribution is independently measurable.
func BenchmarkRedactGatedRegexOnly(b *testing.B) {
	r, err := redact.New(
		nil,
		[]redact.Pattern{
			{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="},
			{Name: "bearer", Regex: `Bearer [A-Za-z0-9]+`, Anchor: "Bearer "},
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	data := largePayload(benchPayloadSize, "sk-deadbeef", "id=")

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Redact(data)
	}
}

// BenchmarkRedactGateMiss measures the cheap path where a pattern's anchor is
// absent, so the regex is skipped entirely. This is the common case for most
// payloads and shows the gate's payoff.
func BenchmarkRedactGateMiss(b *testing.B) {
	r, err := redact.New(
		nil,
		[]redact.Pattern{
			{Name: "absent", Regex: `WONTMATCH-[0-9]+`, Anchor: "WONTMATCH-"},
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	data := largePayload(benchPayloadSize, "sk-deadbeef", "id=")

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Redact(data)
	}
}
