package pipeline_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/pipeline"
)

// recordingStage logs around the next handler. If callNext is false it
// short-circuits with an error without invoking the rest of the chain.
type recordingStage struct {
	name     string
	log      *[]string
	callNext bool
}

func (s recordingStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	*s.log = append(*s.log, s.name+":pre")
	if !s.callNext {
		return nil, errors.New(s.name + " short-circuit")
	}
	res, err := next(ctx, call)
	*s.log = append(*s.log, s.name+":post")
	return res, err
}

func TestChainRunsStagesInOrderAroundBase(t *testing.T) {
	var log []string
	base := func(_ context.Context, _ *domain.Call) (*domain.Result, error) {
		log = append(log, "base")
		return &domain.Result{}, nil
	}
	stages := []domain.Stage{
		recordingStage{name: "a", log: &log, callNext: true},
		recordingStage{name: "b", log: &log, callNext: true},
	}

	h := pipeline.Chain(stages, base)
	if _, err := h(context.Background(), &domain.Call{}); err != nil {
		t.Fatalf("chain returned error: %v", err)
	}

	want := []string{"a:pre", "b:pre", "base", "b:post", "a:post"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("execution order = %v, want %v", log, want)
	}
}

func TestChainShortCircuitSkipsBase(t *testing.T) {
	var log []string
	base := func(_ context.Context, _ *domain.Call) (*domain.Result, error) {
		log = append(log, "base")
		return &domain.Result{}, nil
	}
	stages := []domain.Stage{
		recordingStage{name: "a", log: &log, callNext: true},
		recordingStage{name: "deny", log: &log, callNext: false},
	}

	h := pipeline.Chain(stages, base)
	_, err := h(context.Background(), &domain.Call{})
	if err == nil {
		t.Fatal("expected a short-circuit error")
	}
	for _, e := range log {
		if e == "base" {
			t.Error("base handler ran despite an earlier stage short-circuiting")
		}
	}
}

func TestChainWithNoStagesCallsBase(t *testing.T) {
	called := false
	base := func(_ context.Context, _ *domain.Call) (*domain.Result, error) {
		called = true
		return &domain.Result{}, nil
	}
	h := pipeline.Chain(nil, base)
	if _, err := h(context.Background(), &domain.Call{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("base handler was not called with an empty stage list")
	}
}

// FromDispatcher adapts a domain.Dispatcher into the base handler the chain
// terminates in.
func TestFromDispatcher(t *testing.T) {
	d := dispatcherFunc(func(_ context.Context, c *domain.Call) (*domain.Result, error) {
		return &domain.Result{Content: c.Args}, nil
	})
	h := pipeline.FromDispatcher(d)
	res, err := h(context.Background(), &domain.Call{Args: []byte(`{"x":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Content) != `{"x":1}` {
		t.Errorf("dispatcher result = %s, want the call args echoed", res.Content)
	}
}

type dispatcherFunc func(context.Context, *domain.Call) (*domain.Result, error)

func (f dispatcherFunc) Dispatch(ctx context.Context, c *domain.Call) (*domain.Result, error) {
	return f(ctx, c)
}
