package redact_test

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/redact"
)

// ph is the placeholder string, aliased for terse test expectations.
const ph = redact.Placeholder

// mustNew builds a Redactor and fails the test on error.
func mustNew(t *testing.T, secrets []string, patterns []redact.Pattern) *redact.Redactor {
	t.Helper()
	r, err := redact.New(secrets, patterns)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return r
}

func TestRedactSingleSecret(t *testing.T) {
	r := mustNew(t, []string{"hunter2"}, nil)

	got, count := r.Redact([]byte("password is hunter2 ok"))

	want := []byte("password is " + ph + " ok")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactSameSecretMultipleTimes(t *testing.T) {
	r := mustNew(t, []string{"abc"}, nil)

	got, count := r.Redact([]byte("abc x abc y abc"))

	want := []byte(ph + " x " + ph + " y " + ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestRedactMultipleDistinctSecrets(t *testing.T) {
	r := mustNew(t, []string{"alpha", "bravo", "charlie"}, nil)

	got, count := r.Redact([]byte("alpha then bravo then charlie"))

	want := []byte(ph + " then " + ph + " then " + ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestRedactOverlappingSecrets(t *testing.T) {
	// "abc" spans [0,3) and "bcd" spans [1,4) overlap in "abcd"; the union span
	// [0,4) is replaced once so the output is not corrupted.
	r := mustNew(t, []string{"abc", "bcd"}, nil)

	got, count := r.Redact([]byte("abcd"))

	want := []byte(ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (overlap merged)", count)
	}
}

func TestRedactAdjacentSecrets(t *testing.T) {
	// Directly adjacent secret runs ("ab" then "cd" in "abcd") merge into one
	// redacted span rather than two placeholders.
	r := mustNew(t, []string{"ab", "cd"}, nil)

	got, count := r.Redact([]byte("abcd"))

	want := []byte(ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (adjacent merged)", count)
	}
}

func TestRedactSecretSubstringOfAnother(t *testing.T) {
	// "secret" is a substring of "supersecret". The longest match ending at each
	// position wins, so "supersecret" is redacted as a single span.
	r := mustNew(t, []string{"secret", "supersecret"}, nil)

	got, count := r.Redact([]byte("my supersecret value"))

	want := []byte("my " + ph + " value")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactShorterSecretAloneStillMatches(t *testing.T) {
	// When only the shorter of two nested secrets is present, it must still be
	// redacted (proves the shorter pattern's terminal state is reachable).
	r := mustNew(t, []string{"secret", "supersecret"}, nil)

	got, count := r.Redact([]byte("a secret here"))

	want := []byte("a " + ph + " here")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactEmptyInput(t *testing.T) {
	r := mustNew(t, []string{"abc"}, nil)

	got, count := r.Redact([]byte(""))

	if len(got) != 0 {
		t.Errorf("Redact = %q, want empty", got)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestRedactNilInput(t *testing.T) {
	r := mustNew(t, []string{"abc"}, nil)

	got, count := r.Redact(nil)

	if len(got) != 0 {
		t.Errorf("Redact = %q, want empty", got)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestRedactNoSecretsNoPatternsPassthrough(t *testing.T) {
	r := mustNew(t, nil, nil)

	in := []byte("nothing to redact here")
	got, count := r.Redact(in)

	if !bytes.Equal(got, in) {
		t.Errorf("Redact = %q, want %q", got, in)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestRedactEmptySecretStringsIgnored(t *testing.T) {
	// Empty secret strings must be ignored entirely: they must not produce
	// zero-length matches or insert placeholders between every byte.
	r := mustNew(t, []string{"", "tok", ""}, nil)

	got, count := r.Redact([]byte("a tok b"))

	want := []byte("a " + ph + " b")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactOnlyEmptySecretsPassthrough(t *testing.T) {
	r := mustNew(t, []string{"", ""}, nil)

	in := []byte("abc def")
	got, count := r.Redact(in)

	if !bytes.Equal(got, in) {
		t.Errorf("Redact = %q, want %q", got, in)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestRedactPIIMatchesWhenAnchorPresent(t *testing.T) {
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "bearer", Regex: `Bearer [A-Za-z0-9]+`, Anchor: "Bearer "},
	})

	got, count := r.Redact([]byte("auth: Bearer abc123 end"))

	want := []byte("auth: " + ph + " end")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactPIIMultipleMatches(t *testing.T) {
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "num", Regex: `id=[0-9]+`, Anchor: "id="},
	})

	got, count := r.Redact([]byte("id=1 and id=22 and id=333"))

	want := []byte(ph + " and " + ph + " and " + ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestRedactPIINotConsultedWhenAnchorAbsent(t *testing.T) {
	// The regex would match "127.0.0.1" anywhere, but the anchor "ip=" is absent,
	// so the gate must prevent any replacement. This proves the anchor gate is
	// honored: identical regex-matchable text passes through untouched without
	// the anchor.
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "ip", Regex: `[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`, Anchor: "ip="},
	})

	in := []byte("host 127.0.0.1 reached")
	got, count := r.Redact(in)

	if !bytes.Equal(got, in) {
		t.Errorf("Redact = %q, want unchanged %q (anchor absent)", got, in)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 (anchor gate)", count)
	}
}

func TestRedactPIIAnchorPresentRegexMatches(t *testing.T) {
	// Same regex as the gate-negative test, but now the anchor is present so the
	// regex runs and redacts.
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "ip", Regex: `[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`, Anchor: "ip="},
	})

	got, count := r.Redact([]byte("ip=127.0.0.1 reached"))

	want := []byte("ip=" + ph + " reached")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactPIIEmptyAnchorAlwaysRuns(t *testing.T) {
	// An empty anchor must not short-circuit: bytes.Contains(_, nil) is true, so
	// the regex always runs.
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "word", Regex: `secret`, Anchor: ""},
	})

	got, count := r.Redact([]byte("a secret value"))

	want := []byte("a " + ph + " value")
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactSecretsAndPIITogether(t *testing.T) {
	r := mustNew(t,
		[]string{"topsecretkey"},
		[]redact.Pattern{{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="}},
	)

	got, count := r.Redact([]byte("key topsecretkey for id=42"))

	want := []byte("key " + ph + " for " + ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestRedactPIIRunsOverSecretRedactedBytes(t *testing.T) {
	// Secrets are redacted first; a PII pattern must not re-expose those bytes.
	// Here the secret removal happens before the PII pass sees the data.
	r := mustNew(t,
		[]string{"sk-live-123"},
		[]redact.Pattern{{Name: "all", Regex: `[a-z-]+-[0-9]+`, Anchor: "-"}},
	)

	got, _ := r.Redact([]byte("token sk-live-123 done"))

	// The secret span is removed first; the resulting placeholder contains no
	// pattern match, so the secret bytes are gone from the output.
	if bytes.Contains(got, []byte("sk-live-123")) {
		t.Errorf("Redact = %q, secret leaked", got)
	}
}

func TestRedactDoesNotMutateInput(t *testing.T) {
	r := mustNew(t,
		[]string{"abc"},
		[]redact.Pattern{{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="}},
	)

	in := []byte("abc and id=7")
	original := append([]byte(nil), in...)

	got, _ := r.Redact(in)

	if !bytes.Equal(in, original) {
		t.Errorf("input mutated: got %q, want %q", in, original)
	}
	// And the returned slice must be a distinct allocation, not an alias.
	if len(got) > 0 && &got[0] == &in[0] {
		t.Error("Redact returned a slice aliasing the input backing array")
	}
}

func TestRedactPassthroughDoesNotAliasInput(t *testing.T) {
	// Even when there is nothing to redact, the output must be a distinct slice
	// so callers can treat it as owned and the input stays immutable.
	r := mustNew(t, []string{"absent"}, nil)

	in := []byte("clean payload")
	got, count := r.Redact(in)

	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
	if !bytes.Equal(got, in) {
		t.Errorf("Redact = %q, want %q", got, in)
	}
	if len(got) > 0 && &got[0] == &in[0] {
		t.Error("passthrough output aliases the input backing array")
	}
}

func TestNewErrorOnBadRegex(t *testing.T) {
	_, err := redact.New(nil, []redact.Pattern{
		{Name: "broken", Regex: `([0-9`, Anchor: "x"},
	})
	if err == nil {
		t.Fatal("New: expected error for uncompilable regex, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q does not name the offending pattern", err)
	}
}

func TestNewValidInputs(t *testing.T) {
	r, err := redact.New(
		[]string{"a", "b"},
		[]redact.Pattern{{Name: "ok", Regex: `[0-9]+`, Anchor: "n"}},
	)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("New returned nil Redactor with nil error")
	}
}

func TestRedactPIIAnchorPresentNoRegexMatch(t *testing.T) {
	// The anchor literal is present, so the gate opens, but the full regex does
	// not match. Nothing is replaced and count is 0. This exercises the
	// "gate open, zero matches" branch distinct from the gate-closed branch.
	r := mustNew(t, nil, []redact.Pattern{
		{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="},
	})

	in := []byte("here is id=abc which has no digits")
	got, count := r.Redact(in)

	if !bytes.Equal(got, in) {
		t.Errorf("Redact = %q, want unchanged %q", got, in)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestRedactSecretPrefixAndFullOverlapSameStart(t *testing.T) {
	// Secrets "ab" and "abc" both begin at the same offset in "abc". The longest
	// match per end position yields two spans sharing a start ([0,2) and [0,3));
	// they merge to a single [0,3) redaction. This exercises the merge sort's
	// equal-start tie-break.
	r := mustNew(t, []string{"ab", "abc"}, nil)

	got, count := r.Redact([]byte("abc"))

	if !bytes.Equal(got, []byte(ph)) {
		t.Errorf("Redact = %q, want %q", got, ph)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactSecretAtStartAndEnd(t *testing.T) {
	r := mustNew(t, []string{"xx"}, nil)

	got, count := r.Redact([]byte("xx middle xx"))

	want := []byte(ph + " middle " + ph)
	if !bytes.Equal(got, want) {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestRedactWholeInputIsSecret(t *testing.T) {
	r := mustNew(t, []string{"entire"}, nil)

	got, count := r.Redact([]byte("entire"))

	if !bytes.Equal(got, []byte(ph)) {
		t.Errorf("Redact = %q, want %q", got, ph)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRedactConcurrent(t *testing.T) {
	r := mustNew(t,
		[]string{"alpha", "bravo", "charlie", "supersecret"},
		[]redact.Pattern{
			{Name: "id", Regex: `id=[0-9]+`, Anchor: "id="},
			{Name: "bearer", Regex: `Bearer [A-Za-z0-9]+`, Anchor: "Bearer "},
		},
	)

	in := []byte("alpha bravo charlie supersecret id=42 Bearer tok99 tail")
	want, wantCount := r.Redact(in)

	const goroutines = 64
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			// Each goroutine works on its own copy of the input to also catch any
			// accidental aliasing under concurrency.
			local := append([]byte(nil), in...)
			for i := 0; i < iters; i++ {
				got, count := r.Redact(local)
				if count != wantCount {
					t.Errorf("concurrent count = %d, want %d", count, wantCount)
					return
				}
				if !bytes.Equal(got, want) {
					t.Errorf("concurrent Redact = %q, want %q", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// largePayload builds a ~size-byte payload that embeds occasional secrets and
// PII so the redactor has real work to do. Used by the benchmark.
func largePayload(size int, secret, anchor string) []byte {
	var b bytes.Buffer
	filler := strings.Repeat("the quick brown fox jumps over the lazy dog ", 16)
	for b.Len() < size {
		b.WriteString(filler)
		b.WriteString(secret)
		b.WriteByte(' ')
		fmt.Fprintf(&b, "%s%d ", anchor, b.Len())
	}
	return b.Bytes()[:size]
}

func TestLargePayloadRedacts(t *testing.T) {
	const secret = "sk-deadbeef"
	r := mustNew(t,
		[]string{secret},
		[]redact.Pattern{{Name: "off", Regex: `id=[0-9]+`, Anchor: "id="}},
	)
	data := largePayload(1<<20, secret, "id=")

	got, count := r.Redact(data)
	if count == 0 {
		t.Fatal("expected redactions in large payload")
	}
	if bytes.Contains(got, []byte(secret)) {
		t.Error("secret leaked in large payload output")
	}
}
