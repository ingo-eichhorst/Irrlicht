package opencode

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

	t.Run("Source=ProcessOwnedStore+MetricsReader", func(t *testing.T) {
		s, ok := a.Source.(agent.ProcessOwnedStore)
		if !ok {
			t.Fatalf("Source: want ProcessOwnedStore, got %T", a.Source)
		}
		if s.PathForPID == nil {
			t.Error("ProcessOwnedStore.PathForPID is nil")
		}
		if s.Reader == nil {
			t.Error("ProcessOwnedStore.Reader is nil")
		}
	})

	t.Run("Reader delegates to ComputeMetrics", func(t *testing.T) {
		// Call both with the same nonsense args; both should return the same
		// (state, error) pair because both call ComputeMetrics directly.
		s := a.Source.(agent.ProcessOwnedStore)
		gotState, gotErr := s.Reader.ComputeMetrics("/nonexistent.db", "sess-x")
		wantState, wantErr := c.ComputeMetrics("/nonexistent.db", "sess-x")
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("error-presence mismatch: agent err=%v, config err=%v", gotErr, wantErr)
		}
		if (gotErr != nil) && (wantErr != nil) && gotErr.Error() != wantErr.Error() {
			t.Errorf("error mismatch: agent=%v, config=%v", gotErr, wantErr)
		}
		if (gotState == nil) != (wantState == nil) {
			t.Errorf("state-presence mismatch")
		}
	})

	t.Run("Config.ComputeMetrics not nil (legacy preserves DB-backed path)", func(t *testing.T) {
		if c.ComputeMetrics == nil {
			t.Fatal("legacy Config().ComputeMetrics must remain non-nil")
		}
	})
}
