package backchannel

import "strings"

// UIKind classifies a transcript-invisible UI state detected by reading the
// rendered terminal screen back (issue #732, Phase 3 of #724). Only the
// trust/permission dialog is recognized today; the type leaves room for more
// rendered-only states (error banners, network-stall notices) as later targets.
type UIKind string

const (
	// UIKindNone means nothing recognized is on screen.
	UIKindNone UIKind = ""
	// UIKindTrustDialog means the agent is blocked on an interactive
	// trust/permission dialog — a waiting state rendered only in the TUI that
	// never reaches the transcript (and, for Claude Code, needs no hook).
	UIKindTrustDialog UIKind = "trust_dialog"
)

// trustDialogMarkers are substrings that, when present in a rendered terminal
// screen, indicate the agent is blocked on an interactive trust/permission
// dialog. Kept deliberately narrow and conservative: a false positive forces a
// real session into waiting, so each marker is a full prompt phrase unlikely to
// appear in ordinary agent output. Starts with Claude Code's permission and
// folder-trust prompts; extend per-adapter as onboarding cells are recorded.
var trustDialogMarkers = []string{
	"Do you want to proceed?",
	"Do you trust the files in this folder?",
	"Do you want to allow this tool",
}

// DetectUI inspects a rendered terminal screen and returns the UI state it
// represents, or UIKindNone when nothing recognized is on screen. Pure and
// snapshot-only: it matches against text tmux/kitty already rendered, so no
// VT100 emulation happens here.
func DetectUI(screen string) UIKind {
	for _, m := range trustDialogMarkers {
		if strings.Contains(screen, m) {
			return UIKindTrustDialog
		}
	}
	return UIKindNone
}
