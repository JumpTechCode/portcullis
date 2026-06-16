// Package pipeline composes the per-call security chain.
//
// A chain is an ordered list of domain.Stage middlewares wrapped around a base
// handler, à la net/http middleware: stages run in order on the way in and
// unwind in reverse on the way out. The base handler is backed by a
// domain.Dispatcher. The package owns only the composition mechanism; the
// concrete stages (policy, redaction, secrets, resilience, audit) are assembled
// and ordered at the composition root, so reordering or inserting a stage is a
// wiring change there, never an edit here (design §3).
package pipeline

import (
	"context"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Chain composes stages around base into a single handler. stages[0] is the
// outermost; the chain terminates in base. A nil or empty stage list yields base
// unchanged.
func Chain(stages []domain.Stage, base domain.HandlerFunc) domain.HandlerFunc {
	h := base
	for i := len(stages) - 1; i >= 0; i-- {
		stage := stages[i]
		next := h
		h = func(ctx context.Context, call *domain.Call) (*domain.Result, error) {
			return stage.Handle(ctx, call, next)
		}
	}
	return h
}

// FromDispatcher adapts a Dispatcher into the base handler a chain terminates in.
func FromDispatcher(d domain.Dispatcher) domain.HandlerFunc {
	return d.Dispatch
}
