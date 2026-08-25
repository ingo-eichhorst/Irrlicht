package aider

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

// #1800 listed aider among the adapters with "no error signal at all". It has
// one — errorRE / flushErrorTurn — and this is the test that says so.
func TestParser_LLMErrorBlockquote_IsASessionError(t *testing.T) {
	p := &Parser{}
	// Open a turn first: flushErrorTurn only fires while one is open.
	p.ParseLineRaw("> Model: gpt-4o with diff edit format")
	p.ParseLineRaw("#### fix the bug")

	ev := p.ParseLineRaw("> litellm.BadRequestError: OpenAIException - context length exceeded")
	if ev == nil {
		t.Fatal("an LLM-layer error blockquote must close the turn")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" (unchanged)", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — the error text reached the UI only as prose")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", ev.SessionError.Phase)
	}
	if ev.SessionError.Message == "" {
		t.Error("Message is empty")
	}
	// Plaintext chat history carries no status code anywhere; nil is the
	// honest answer and #1798 made these fields pointers so it can be given.
	if ev.SessionError.HTTPStatus != nil {
		t.Errorf("HTTPStatus = %v, want nil — aider's transcript has no status code to read",
			*ev.SessionError.HTTPStatus)
	}
}

// LOCK — an ordinary turn end must stay clean.
func TestParser_NormalTurnEnd_HasNoSessionError(t *testing.T) {
	p := &Parser{}
	p.ParseLineRaw("> Model: gpt-4o with diff edit format")
	p.ParseLineRaw("#### fix the bug")
	p.ParseLineRaw("Here is the fix.")

	// The `> Tokens:` line reports usage and closes the turn's accounting; it
	// emits assistant_message rather than turn_done (aider settles through
	// IdleFlush). Either way it must carry no failure.
	ev := p.ParseLineRaw("> Tokens: 1.2k sent, 300 received. Cost: $0.01 message, $0.05 session.")
	if ev == nil {
		t.Fatal("precondition: a Tokens line must produce an event")
	}
	if ev.SessionError != nil {
		t.Errorf("a normal turn end must carry no session error, got %+v", ev.SessionError)
	}
}
