package control

import (
	"context"
	"reflect"
	"testing"

	"irrlicht/core/domain/session"
)

type fakeRepo struct {
	state *session.SessionState
	err   error
}

func (r *fakeRepo) Load(_ string) (*session.SessionState, error) { return r.state, r.err }
func (r *fakeRepo) Save(_ *session.SessionState) error           { return nil }
func (r *fakeRepo) Delete(_ string) error                        { return nil }
func (r *fakeRepo) ListAll() ([]*session.SessionState, error)    { return nil, nil }

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
		{"plain terminal (no target)", &session.Launcher{TermProgram: "Apple_Terminal", TTY: "/dev/ttys001"}, backendNone},
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
	c := NewController(repo, nopLog{})
	var ran command
	c.run = func(_ context.Context, cmd command) error { ran = cmd; return nil }

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

func TestControllerNoBackend(t *testing.T) {
	repo := &fakeRepo{state: &session.SessionState{
		SessionID: "abc",
		Launcher:  &session.Launcher{TermProgram: "Apple_Terminal", TTY: "/dev/ttys001"},
	}}
	c := NewController(repo, nopLog{})
	if c.Controllable("abc") {
		t.Error("plain Terminal.app (no CLI backend target) should not be controllable in phase 1")
	}
	if err := c.SendInput("abc", []byte("x")); err == nil {
		t.Error("expected error sending to a session with no controllable backend")
	}
}
