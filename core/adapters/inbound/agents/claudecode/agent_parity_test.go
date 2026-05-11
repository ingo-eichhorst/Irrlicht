package claudecode

import (
	"reflect"
	"testing"

	"irrlicht/core/domain/agent"
)

// TestAgentParityWithConfig asserts that Agent() (new shape introduced in
// #159 Phase A) and Config() (legacy shape) produce equivalent data for
// every downstream-consumed field. Locked in here so PR2/PR3 can delete
// Config() with the parity test as the safety net.
func TestAgentParityWithConfig(t *testing.T) {
	a := Agent()
	c := Config()

	t.Run("Identity.Name", func(t *testing.T) {
		if a.Identity.Name != c.Name {
			t.Errorf("name: got %q, want %q", a.Identity.Name, c.Name)
		}
	})
	t.Run("Identity.DisplayName", func(t *testing.T) {
		if a.Identity.DisplayName != c.DisplayName {
			t.Errorf("display_name: got %q, want %q", a.Identity.DisplayName, c.DisplayName)
		}
	})
	t.Run("Identity.IconSVGLight", func(t *testing.T) {
		if a.Identity.IconSVGLight != c.IconSVGLight {
			t.Errorf("icon_svg_light mismatch")
		}
	})
	t.Run("Identity.IconSVGDark", func(t *testing.T) {
		if a.Identity.IconSVGDark != c.IconSVGDark {
			t.Errorf("icon_svg_dark mismatch")
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

	t.Run("Source=FilesUnderRoot", func(t *testing.T) {
		s, ok := a.Source.(agent.FilesUnderRoot)
		if !ok {
			t.Fatalf("Source: want FilesUnderRoot, got %T", a.Source)
		}
		if s.Dir != c.RootDir {
			t.Errorf("FilesUnderRoot.Dir: got %q, want %q", s.Dir, c.RootDir)
		}
		if _, ok := s.Parser.(agent.JSONLineParser); !ok {
			t.Fatalf("Source.Parser: want JSONLineParser, got %T", s.Parser)
		}
	})

	t.Run("JSONLineParser.NewParser returns LineParser", func(t *testing.T) {
		s := a.Source.(agent.FilesUnderRoot)
		p := s.Parser.(agent.JSONLineParser).NewParser()
		if p == nil {
			t.Fatal("NewParser returned nil")
		}
	})

	t.Run("Parser implements SubagentCounter", func(t *testing.T) {
		s := a.Source.(agent.FilesUnderRoot)
		p := s.Parser.(agent.JSONLineParser).NewParser()
		if _, ok := p.(agent.SubagentCounter); !ok {
			t.Fatal("claudecode.Parser must implement agent.SubagentCounter (delegates to CountOpenSubagents)")
		}
	})

	t.Run("Parser implements PendingContributor", func(t *testing.T) {
		s := a.Source.(agent.FilesUnderRoot)
		p := s.Parser.(agent.JSONLineParser).NewParser()
		if _, ok := p.(agent.PendingContributor); !ok {
			t.Fatal("claudecode.Parser must implement agent.PendingContributor")
		}
	})

	t.Run("Config.CountOpenSubagents not nil", func(t *testing.T) {
		if c.CountOpenSubagents == nil {
			t.Fatal("legacy Config().CountOpenSubagents must still be non-nil for compatibility")
		}
	})
}
