package aider

import (
	"reflect"
	"testing"

	"irrlicht/core/domain/agent"
)

func TestAgentParityWithConfig(t *testing.T) {
	a := Agent()
	c := Config()

	t.Run("Identity", func(t *testing.T) {
		if a.Identity.Name != c.Name {
			t.Errorf("name: got %q, want %q", a.Identity.Name, c.Name)
		}
		if a.Identity.DisplayName != c.DisplayName {
			t.Errorf("display_name: got %q, want %q", a.Identity.DisplayName, c.DisplayName)
		}
		if a.Identity.IconSVGLight != c.IconSVGLight {
			t.Error("icon_svg_light mismatch")
		}
		if a.Identity.IconSVGDark != c.IconSVGDark {
			t.Error("icon_svg_dark mismatch")
		}
	})

	t.Run("Process.Match=CommandPattern", func(t *testing.T) {
		m, ok := a.Process.Match.(agent.CommandPattern)
		if !ok {
			t.Fatalf("Process.Match: want CommandPattern, got %T", a.Process.Match)
		}
		if m.Regex.String() != c.CommandLineMatch {
			t.Errorf("CommandPattern regex: got %q, want %q", m.Regex.String(), c.CommandLineMatch)
		}
	})
	t.Run("Process.PIDForSession identity", func(t *testing.T) {
		if reflect.ValueOf(a.Process.PIDForSession).Pointer() != reflect.ValueOf(c.DiscoverPID).Pointer() {
			t.Error("PIDForSession is not the same function as Config().DiscoverPID")
		}
	})

	t.Run("Source=FilesUnderCWD+RawLineParser", func(t *testing.T) {
		s, ok := a.Source.(agent.FilesUnderCWD)
		if !ok {
			t.Fatalf("Source: want FilesUnderCWD, got %T", a.Source)
		}
		if s.Filename != c.TranscriptFilename {
			t.Errorf("FilesUnderCWD.Filename: got %q, want %q", s.Filename, c.TranscriptFilename)
		}
		if s.Parser.ParseLineRaw == nil {
			t.Error("RawLineParser.ParseLineRaw is nil")
		}
		if s.Parser.IdleFlush == nil {
			t.Error("RawLineParser.IdleFlush is nil")
		}
	})
}
