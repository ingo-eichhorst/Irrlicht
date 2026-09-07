package desktopdriver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fastFirstTurn is a session whose first turn is already OVER by the time the
// driver starts watching it. Measured on cell 2-8, 2026-09-07:
//
//	20:23:43.339  ready    (new session created)
//	20:23:43.342  working  (transcript activity)      <- 3ms of it
//	20:24:29.968  waiting  (turn ended with a cue)
//
// The driver was still resolving the Desktop registry row through all of that,
// and began watching afterwards.
type fastFirstTurn struct {
	fakeRuntime
	workingAsked int
}

func (runtime *fastFirstTurn) WaitIrrlichtState(
	ctx context.Context, owned OwnedSession, state string,
) (SessionObservation, error) {
	if state == "working" {
		runtime.workingAsked++
		return SessionObservation{}, errors.New(
			"Irrlicht session state working was not observed before its deadline")
	}
	return runtime.fakeRuntime.WaitIrrlichtState(ctx, owned, state)
}

func (runtime *fastFirstTurn) WaitIrrlichtTurnStart(
	_ context.Context, _ OwnedSession,
) (SessionObservation, error) {
	return SessionObservation{SessionID: "fast", State: "waiting"}, nil
}

// RED-FIRST: with bindOwnership waiting for `working`, this fails with
// `Irrlicht session state working was not observed before its deadline` — the
// exact message cell 2-8 produced after burning its whole 12-minute budget.
func TestAFirstTurnThatIsAlreadyOverStillCounts(t *testing.T) {
	runtime := &fastFirstTurn{}
	request := validRunRequest()

	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v; a first turn that reached waiting before the driver looked is still a turn", err)
	}
	if runtime.workingAsked != 0 {
		t.Errorf("the driver asked for `working` %d time(s); a first turn must not be gated on catching a transient",
			runtime.workingAsked)
	}
}

// `ready` is the state a session is BORN in, so accepting it would let a submit
// that never landed pass as a turn that ran.
type bornReady struct{ fakeRuntime }

func (runtime *bornReady) WaitIrrlichtTurnStart(
	_ context.Context, _ OwnedSession,
) (SessionObservation, error) {
	return SessionObservation{SessionID: "newborn", State: "ready"}, nil
}

func TestASessionStillInItsBirthStateIsNotProofATurnRan(t *testing.T) {
	_, err := Run(context.Background(), &bornReady{}, validRunRequest())
	if err == nil {
		t.Fatal("Run() = nil; a session reported `ready` has not been shown to have run a turn")
	}
	if !strings.Contains(err.Error(), "not proof it ran") {
		t.Fatalf("Run() error = %v; want it to say `ready` is not proof the turn ran", err)
	}
}
