package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := stat(path)
	if err != nil {
		t.Fatalf("stat returned error: %v", err)
	}
	if !st.ok {
		t.Fatal("stat reported an existing file as absent")
	}
	if st.size != 5 {
		t.Errorf("size = %d, want 5", st.size)
	}
}

func TestStatMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	st, err := stat(path)
	if err != nil {
		t.Fatalf("stat on a missing file returned error %v, want nil", err)
	}
	if st.ok {
		t.Error("stat reported a missing file as present")
	}
}

func TestChanged(t *testing.T) {
	base := time.Unix(1000, 0)
	present := fileState{ok: true, modTime: base, size: 10}
	cases := []struct {
		name string
		a, b fileState
		want bool
	}{
		{"both absent", fileState{}, fileState{}, false},
		{"unchanged", present, present, false},
		{"newer modtime", present, fileState{ok: true, modTime: base.Add(time.Second), size: 10}, true},
		{"different size", present, fileState{ok: true, modTime: base, size: 11}, true},
		{"appeared", fileState{}, present, true},
		{"vanished", present, fileState{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changed(c.a, c.b); got != c.want {
				t.Errorf("changed(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}
