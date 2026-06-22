package control

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func TestCaptureCommandBuilders(t *testing.T) {
	tmuxL := &session.Launcher{TmuxPane: "%3", TmuxSocket: "/tmp/tmux-501/default"}
	tmuxNoSock := &session.Launcher{TmuxPane: "%7"}
	kittyL := &session.Launcher{KittyListenOn: "unix:/tmp/mykitty", KittyWindowID: "12"}

	cases := []struct {
		name string
		got  command
		want command
	}{
		{
			"tmux capture with socket",
			tmuxCapture(tmuxL),
			command{name: "tmux", args: []string{"-S", "/tmp/tmux-501/default", "capture-pane", "-t", "%3", "-p"}},
		},
		{
			"tmux capture without socket",
			tmuxCapture(tmuxNoSock),
			command{name: "tmux", args: []string{"capture-pane", "-t", "%7", "-p"}},
		},
		{
			"kitty capture",
			kittyCapture(kittyL),
			command{name: "kitten", args: []string{"@", "--to", "unix:/tmp/mykitty", "get-text", "--match", "id:12"}},
		},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s:\n got = %+v\nwant = %+v", c.name, c.got, c.want)
		}
	}
}

func TestCaptureScreenDispatch(t *testing.T) {
	cases := []struct {
		name      string
		launcher  *session.Launcher
		wantName  string
		wantErrIs error
	}{
		{"tmux", &session.Launcher{TmuxPane: "%1"}, "tmux", nil},
		{"kitty", &session.Launcher{KittyListenOn: "unix:/x", KittyWindowID: "9"}, "kitten", nil},
		{"applescript not readable", &session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:U"}, "", outbound.ErrNotReadable},
		{"no backend not readable", &session.Launcher{TermProgram: "vscode"}, "", outbound.ErrNotReadable},
	}
	for _, c := range cases {
		var ran command
		r := &Reader{
			repo:   &fakeRepo{state: &session.SessionState{SessionID: "s", Launcher: c.launcher}},
			logger: nopLog{},
			capture: func(_ context.Context, cmd command) ([]byte, error) {
				ran = cmd
				return []byte("SCREEN"), nil
			},
		}
		out, err := r.CaptureScreen("s")
		if c.wantErrIs != nil {
			if !errors.Is(err, c.wantErrIs) {
				t.Errorf("%s: err = %v, want Is %v", c.name, err, c.wantErrIs)
			}
			if ran.name != "" {
				t.Errorf("%s: expected no shell-out, ran %q", c.name, ran.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
		}
		if ran.name != c.wantName {
			t.Errorf("%s: ran %q, want %q", c.name, ran.name, c.wantName)
		}
		if string(out) != "SCREEN" {
			t.Errorf("%s: out = %q, want SCREEN", c.name, out)
		}
	}
}

func TestReadable(t *testing.T) {
	cases := []struct {
		name     string
		launcher *session.Launcher
		want     bool
	}{
		{"tmux", &session.Launcher{TmuxPane: "%1"}, true},
		{"kitty", &session.Launcher{KittyListenOn: "unix:/x", KittyWindowID: "9"}, true},
		{"applescript", &session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0:U"}, false},
		{"none", &session.Launcher{TermProgram: "vscode"}, false},
	}
	for _, c := range cases {
		r := NewReader(&fakeRepo{state: &session.SessionState{SessionID: "s", Launcher: c.launcher}}, nopLog{})
		if got := r.Readable("s"); got != c.want {
			t.Errorf("%s: Readable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReadableUnknownSession(t *testing.T) {
	r := NewReader(&fakeRepo{err: errors.New("not found")}, nopLog{})
	if r.Readable("missing") {
		t.Error("Readable on a missing session should be false")
	}
	if _, err := r.CaptureScreen("missing"); err == nil {
		t.Error("CaptureScreen on a missing session should error")
	}
}
