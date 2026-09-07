package desktopdriver

// A turn that ends by asking the user something ends `waiting`, not `ready`.
// Everything downstream of it has to accept that, or a recipe with a second
// send hangs on a state that is never coming.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// blockedRuntime is a session whose turns end at a blocking dialog. It NEVER
// reports `ready` — which is what the live daemon does for cell 2-27, whose
// first turn ends at a permission dialog and stays there until it is answered.
type blockedRuntime struct {
	fakeRuntime
	readyWaits int
}

func (runtime *blockedRuntime) WaitIrrlichtState(
	ctx context.Context, owned OwnedSession, state string,
) (SessionObservation, error) {
	if state == "ready" {
		runtime.readyWaits++
		<-ctx.Done()
		return SessionObservation{}, ctx.Err()
	}
	return runtime.fakeRuntime.WaitIrrlichtState(ctx, owned, state)
}

// RED-FIRST. Cell 2-27 spent its whole twenty-minute budget here on 2026-09-07:
//
//	Desktop recipe step 4 (send): wait for Irrlicht state ready timed out after
//	20m0s: context deadline exceeded
//
// Its first turn ended `waiting` at a blocking dialog. wait_turn already
// accepted either completed-turn state; the pre-send readiness wait did not,
// and it is the same question — is the turn over and will the composer take a
// prompt? — to which `waiting` is also yes.
func TestASecondSendProceedsWhenTheTurnEndedWaiting(t *testing.T) {
	runtime := &blockedRuntime{}
	runtime.turnEndState = "waiting"
	request := validRunRequest()
	request.Prompt = ""
	request.TurnTimeout = 2 * time.Second
	request.Script = []Step{
		{Type: StepSend, Text: "first"},
		{Type: StepWaitTurn},
		{Type: StepSend, Text: "second"},
		{Type: StepWaitTurn},
	}
	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v; the turn ended waiting, which is a turn that ended", err)
	}
	if runtime.readyWaits != 0 {
		t.Errorf("the run waited for `ready` %d time(s); this session never reports it", runtime.readyWaits)
	}
	sends := 0
	for _, step := range runtime.steps {
		if step == "set_prompt" {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("the recipe typed %d prompt(s), want 2", sends)
	}
}

// The lock on the other side: a state that is NOT a completed turn must still
// be refused rather than treated as one.
func TestTurnEndRefusesANonTerminalState(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.turnEndState = "working"
	request := validRunRequest()
	_, err := Run(context.Background(), runtime, request)
	if err == nil {
		t.Fatal("Run() accepted `working` as a completed turn")
	}
	if !strings.Contains(err.Error(), "non-terminal state") {
		t.Fatalf("Run() error = %v, want it to name the non-terminal state", err)
	}
}
