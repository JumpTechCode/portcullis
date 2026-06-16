package config_test

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/JumpTechCode/portcullis/internal/config"
)

func TestDurationUnmarshalsString(t *testing.T) {
	var d config.Duration
	if err := yaml.Unmarshal([]byte(`"2s"`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Duration() != 2*time.Second {
		t.Errorf("Duration() = %v, want 2s", d.Duration())
	}
}

func TestDurationRejectsInvalidString(t *testing.T) {
	var d config.Duration
	if err := yaml.Unmarshal([]byte(`"not-a-duration"`), &d); err == nil {
		t.Error("unmarshal of an invalid duration returned nil error")
	}
}

func TestDurationRejectsNonString(t *testing.T) {
	var d config.Duration
	if err := yaml.Unmarshal([]byte(`123`), &d); err == nil {
		t.Error("unmarshal of a non-string duration returned nil error")
	}
}

func TestDurationRejectsNonScalar(t *testing.T) {
	var d config.Duration
	if err := yaml.Unmarshal([]byte("[1, 2, 3]"), &d); err == nil {
		t.Error("unmarshal of a non-scalar duration returned nil error")
	}
}

func TestDurationString(t *testing.T) {
	if got := config.Duration(90 * time.Second).String(); got != "1m30s" {
		t.Errorf("String() = %q, want 1m30s", got)
	}
}
