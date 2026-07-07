package control

import (
	"context"
	"reflect"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

type fakeRepo struct {
	state *session.SessionState
	err   error
}

func (r *fakeRepo) Load(_ string) (*session.SessionState, error) { return r.state, r.err }
func (r *fakeRepo) Save(_ *session.SessionState) error           { return nil }
func (r *fakeRepo) Delete(_ string) error                        { return nil }
func (r *fakeRepo) ListAll() ([]*session.SessionState, error)    { return nil, nil }

type fakePush struct{ msgs []outbound.PushMessage }

func (p *fakePush) Broadcast(m outbound.PushMessage)        { p.msgs = append(p.msgs, m) }
func (p *fakePush) Subscribe() chan outbound.PushMessage    { return make(chan outbound.PushMessage) }
func (p *fakePush) Unsubscribe(_ chan outbound.PushMessage) {}

type nopLog struct{}

func (nopLog) LogInfo(_, _, _ string)                                  {}
func (nopLog) LogError(_, _, _ string)                                 {}
func (nopLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (nopLog) Close() error                                            { return nil }

func TestResolveBackend(t *testing.T) {
	cases := []struct {
		name string
		l    *session.Launcher
		want backend
	}{
		{"nil", nil, backendNone},
		{"tmux pane", &session.Launcher{TmuxPane: "%3"}, backendTmux},
		{"tmux wins over kitty", &session.Launcher{TmuxPane: "%3", KittyListenOn: "unix:/x", KittyWindowID: "12"}, backendTmux},
		{"kitty both fields", &session.Launcher{KittyListenOn: "unix:/x", KittyWindowID: "12"}, backendKitty},
		{"kitty missing window", &session.Launcher{KittyListenOn: "unix:/x"}, backendNone},
		{"iterm session", &session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:UUID"}, backendAppleScript},
		{"terminal.app by tty", &session.Launcher{TermProgram: "Apple_Terminal", TTY: "/dev/ttys001"}, backendAppleScript},
		{"iterm without session id", &session.Launcher{TermProgram: "iTerm.app"}, backendNone},
		{"unsupported host (vscode)", &session.Launcher{TermProgram: "vscode"}, backendNone},
	}
	for _, c := range cases {
		if got := resolveBackend(c.l); got != c.want {
			t.Errorf("%s: resolveBackend = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCommandBuilders(t *testing.T) {
	tmuxL := &session.Launcher{TmuxPane: "%3", TmuxSocket: "/tmp/tmux-501/default"}
	tmuxNoSock := &session.Launcher{TmuxPane: "%7"}
	kittyL := &session.Launcher{KittyListenOn: "unix:/tmp/mykitty", KittyWindowID: "12"}

	cases := []struct {
		name string
		got  command
		want command
	}{
		{
			"tmux input with socket",
			tmuxInput(tmuxL, []byte("hello\r")),
			command{"tmux", []string{"-S", "/tmp/tmux-501/default", "send-keys", "-t", "%3", "-l", "--", "hello\r"}},
		},
		{
			"tmux input no socket",
			tmuxInput(tmuxNoSock, []byte("hi")),
			command{"tmux", []string{"send-keys", "-t", "%7", "-l", "--", "hi"}},
		},
		{
			"tmux interrupt",
			tmuxInterrupt(tmuxL),
			command{"tmux", []string{"-S", "/tmp/tmux-501/default", "send-keys", "-t", "%3", "C-c"}},
		},
		{
			"kitty input",
			kittyInput(kittyL, []byte("ls\r")),
			command{"kitten", []string{"@", "--to", "unix:/tmp/mykitty", "send-text", "--match", "id:12", "--", "ls\r"}},
		},
		{
			"kitty interrupt",
			kittyInterrupt(kittyL),
			command{"kitten", []string{"@", "--to", "unix:/tmp/mykitty", "send-text", "--match", "id:12", "--", "\x03"}},
		},
	}
	for _, c := range cases {
		if c.got.name != c.want.name || !reflect.DeepEqual(c.got.args, c.want.args) {
			t.Errorf("%s:\n got  %s %q\n want %s %q", c.name, c.got.name, c.got.args, c.want.name, c.want.args)
		}
	}
}

func TestControllerDelegatesToBackend(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc",
		Launcher:  &session.Launcher{TmuxPane: "%3"},
	}}
	c := NewController(repo, &fakePush{}, nopLog{})
	var ran command
	c.run = func(_ context.Context, cmd command) ([]byte, error) { ran = cmd; return nil, nil }

	if !c.Controllable("abc") {
		t.Fatal("tmux session should be controllable")
	}
	if err := c.SendInput("abc", []byte("x")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if ran.name != "tmux" || ran.args[len(ran.args)-1] != "x" {
		t.Errorf("expected tmux send-keys with payload x, got %s %q", ran.name, ran.args)
	}
}

func TestControllerAppleScriptBroadcasts(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc",
		Launcher:  &session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:UUID"},
	}}
	push := &fakePush{}
	c := NewController(repo, push, nopLog{})
	c.run = func(_ context.Context, _ command) ([]byte, error) {
		t.Fatal("AppleScript backend must not shell out")
		return nil, nil
	}

	if !c.Controllable("abc") {
		t.Fatal("iTerm2 session should be controllable via the app")
	}
	if err := c.SendInput("abc", []byte("hi")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if len(push.msgs) != 1 || push.msgs[0].Type != outbound.PushTypeInputRequested {
		t.Fatalf("expected one input_requested broadcast, got %v", push.msgs)
	}
	if push.msgs[0].Input == nil || push.msgs[0].Input.Action != outbound.InputActionInput || push.msgs[0].Input.Data != "hi" {
		t.Errorf("unexpected input payload: %+v", push.msgs[0].Input)
	}
}

// TestControllerSendCommandSubmits locks the per-backend submit ownership
// (issue #754): tmux/kitty get a trailing CR appended; the AppleScript path
// broadcasts the bare command (the app auto-submits, so a CR would double).
func TestControllerSendCommandSubmits(t *testing.T) {
	t.Run("tmux appends CR", verifySendCommandTmuxAppendsCR)
	t.Run("kitty appends CR", verifySendCommandKittyAppendsCR)
	t.Run("AppleScript broadcasts bare command", verifySendCommandAppleScriptBroadcastsBare)
	t.Run("no backend errors", verifySendCommandNoBackendErrors)
}

func verifySendCommandTmuxAppendsCR(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc", Launcher: &session.Launcher{TmuxPane: "%3"},
	}}
	c := NewController(repo, &fakePush{}, nopLog{})
	var ran command
	c.run = func(_ context.Context, cmd command) ([]byte, error) { ran = cmd; return nil, nil }
	if err := c.SendCommand("abc", "/compact"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if got := ran.args[len(ran.args)-1]; got != "/compact\r" {
		t.Errorf("tmux payload = %q, want %q", got, "/compact\r")
	}
}

func verifySendCommandKittyAppendsCR(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc", Launcher: &session.Launcher{KittyListenOn: "unix:/x", KittyWindowID: "12"},
	}}
	c := NewController(repo, &fakePush{}, nopLog{})
	var ran command
	c.run = func(_ context.Context, cmd command) ([]byte, error) { ran = cmd; return nil, nil }
	if err := c.SendCommand("abc", "/compact"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if got := ran.args[len(ran.args)-1]; got != "/compact\r" {
		t.Errorf("kitty payload = %q, want %q", got, "/compact\r")
	}
}

func verifySendCommandAppleScriptBroadcastsBare(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc", Launcher: &session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:UUID"},
	}}
	push := &fakePush{}
	c := NewController(repo, push, nopLog{})
	c.run = func(_ context.Context, _ command) ([]byte, error) {
		t.Fatal("AppleScript backend must not shell out")
		return nil, nil
	}
	if err := c.SendCommand("abc", "/compact"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if len(push.msgs) != 1 || push.msgs[0].Input == nil {
		t.Fatalf("expected one input_requested broadcast, got %v", push.msgs)
	}
	// No trailing CR — the app's write-text/do-script auto-submits.
	if got := push.msgs[0].Input.Data; got != "/compact" {
		t.Errorf("AppleScript payload = %q, want %q (no CR)", got, "/compact")
	}
}

func verifySendCommandNoBackendErrors(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc", Launcher: &session.Launcher{TermProgram: "vscode"},
	}}
	c := NewController(repo, &fakePush{}, nopLog{})
	if err := c.SendCommand("abc", "/compact"); err == nil {
		t.Error("expected error for a session with no controllable backend")
	}
}

func TestControllerNoBackend(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc",
		Launcher:  &session.Launcher{TermProgram: "vscode"}, // no scriptable target
	}}
	c := NewController(repo, &fakePush{}, nopLog{})
	if c.Controllable("abc") {
		t.Error("a session with no scriptable backend target should not be controllable")
	}
	if err := c.SendInput("abc", []byte("x")); err == nil {
		t.Error("expected error sending to a session with no controllable backend")
	}
}
