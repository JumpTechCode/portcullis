package domain_test

import (
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

func TestIdentityString(t *testing.T) {
	id := domain.Identity{ID: "claude-desktop"}
	if got, want := id.String(), "claude-desktop"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
