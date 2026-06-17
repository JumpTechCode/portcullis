package main

import (
	"strings"
	"testing"
)

func TestRunRejectsMissingConfig(t *testing.T) {
	err := run("does-not-exist.yaml")
	if err == nil {
		t.Fatal("expected run to fail for a missing config file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error should name the config path, got %v", err)
	}
}
