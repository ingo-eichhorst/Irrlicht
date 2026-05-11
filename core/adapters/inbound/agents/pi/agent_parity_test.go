package pi

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

	t.Run("Process.Match=ExactName", func(t *testing.T) {
		m, ok := a.Process.Match.(agent.ExactName)
		if !ok {
			t.Fatalf("Process.Match: want ExactName, got %T", a.Process.Match)
		}
		if m.Name != c.ProcessName {
			t.Errorf("ExactName.Name: got %q, want %q", m.Name, c.ProcessName)
		}
	})
	t.Run("Process.PIDForSession identity", func(t *testing.T) {
		if reflect.ValueOf(a.Process.PIDForSession).Pointer() != reflect.ValueOf(c.DiscoverPID).Pointer() {
			t.Error("PIDForSession is not the same function as Config().DiscoverPID")
		}
	})

	t.Run("Source=FilesUnderRoot+JSONLineParser", func(t *testing.T) {
		s, ok := a.Source.(agent.FilesUnderRoot)
		if !ok {
			t.Fatalf("Source: want FilesUnderRoot, got %T", a.Source)
		}
		if s.Dir != c.RootDir {
			t.Errorf("FilesUnderRoot.Dir: got %q, want %q", s.Dir, c.RootDir)
		}
		jp, ok := s.Parser.(agent.JSONLineParser)
		if !ok {
			t.Fatalf("Source.Parser: want JSONLineParser, got %T", s.Parser)
		}
		if jp.NewParser() == nil {
			t.Fatal("JSONLineParser.NewParser returned nil")
		}
	})
}
