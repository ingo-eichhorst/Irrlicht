package desktopdriver

// Which deadline bounds which wait. The driver has two, and giving a wait the
// wrong one is invisible until the day something legitimately takes longer.

import (
	"context"
	"testing"
	"time"
)

// budgetProbe records how much time each wait was actually given.
type budgetProbe struct {
	fakeRuntime
	budgets map[string]time.Duration
}

func (probe *budgetProbe) record(name string, ctx context.Context) {
	if probe.budgets == nil {
		probe.budgets = map[string]time.Duration{}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		probe.budgets[name] = -1
		return
	}
	probe.budgets[name] = time.Until(deadline)
}

func (probe *budgetProbe) WaitComposer(ctx context.Context, workspace string) error {
	probe.record("composer", ctx)
	return probe.fakeRuntime.WaitComposer(ctx, workspace)
}

func (probe *budgetProbe) WaitOwnedSession(
	ctx context.Context, baseline Baseline, workspace string,
) (OwnedSession, error) {
	probe.record("owned", ctx)
	return probe.fakeRuntime.WaitOwnedSession(ctx, baseline, workspace)
}

func (probe *budgetProbe) WaitHook(ctx context.Context, owned OwnedSession) error {
	probe.record("hook", ctx)
	return probe.fakeRuntime.WaitHook(ctx, owned)
}

func (probe *budgetProbe) WaitIrrlichtState(
	ctx context.Context, owned OwnedSession, state string,
) (SessionObservation, error) {
	probe.record("state:"+state, ctx)
	return probe.fakeRuntime.WaitIrrlichtState(ctx, owned, state)
}

func (probe *budgetProbe) WaitIrrlichtTurnEnd(
	ctx context.Context, owned OwnedSession,
) (SessionObservation, error) {
	probe.record("turn-end", ctx)
	return probe.fakeRuntime.WaitIrrlichtTurnEnd(ctx, owned)
}

// RED-FIRST: every wait below used StepTimeout, so the two subagent cells 3-1
// and 3-2 both died on 2026-09-06 with `wait for Irrlicht state ready timed out
// after 1m30s` while the turn each was recording was still running correctly.
// 1m30s is what `min(cell timeout / 3, 90s)` produces; nothing measured says a
// subagent turn fits in it.
//
// A wait the INTERFACE owes keeps the step budget, because failing fast on a
// composer that should already be on screen is the point of having one.
func TestTurnWaitsUseTheTurnBudgetAndInterfaceWaitsUseTheStepBudget(t *testing.T) {
	probe := &budgetProbe{}
	request := validRunRequest()
	request.OverallTimeout = 30 * time.Second
	request.StepTimeout = 250 * time.Millisecond
	request.TurnTimeout = 10 * time.Second
	if _, err := Run(context.Background(), probe, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	interfaceWaits := []string{"composer", "owned"}
	turnWaits := []string{"hook", "state:working", "turn-end"}
	for _, name := range append(append([]string{}, interfaceWaits...), turnWaits...) {
		if _, seen := probe.budgets[name]; !seen {
			t.Fatalf("the %q wait never ran; this check cannot compare a budget it never saw", name)
		}
	}
	for _, name := range interfaceWaits {
		if budget := probe.budgets[name]; budget > request.StepTimeout {
			t.Errorf("%q was given %s; an interface wait keeps the %s step budget",
				name, budget, request.StepTimeout)
		}
	}
	for _, name := range turnWaits {
		budget := probe.budgets[name]
		if budget <= request.StepTimeout {
			t.Errorf("%q was given %s, which is the %s step budget; a turn takes as long as the agent takes",
				name, budget, request.StepTimeout)
		}
		if budget > request.TurnTimeout {
			t.Errorf("%q was given %s, more than the %s turn budget", name, budget, request.TurnTimeout)
		}
	}
}

// Both budgets are required. A caller that sets only one gets an error, not the
// other one silently standing in for it.
func TestRunRequiresATurnBudget(t *testing.T) {
	request := validRunRequest()
	request.TurnTimeout = 0
	if _, err := Run(context.Background(), &fakeRuntime{}, request); err == nil {
		t.Fatal("Run() accepted a request with no turn budget")
	}
}
