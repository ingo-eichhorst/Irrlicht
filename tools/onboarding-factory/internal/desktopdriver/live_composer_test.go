package desktopdriver

// Opening the Desktop composer, and everything the wait for it must tolerate:
// the workspace trust prompt, an animating sheet, and a tree mid-render.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenComposerUsesOfficialRouteWithOnlyExactWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var opened string
	runtime := &LiveRuntime{openDeepLink: func(_ context.Context, target string) error {
		opened = target
		return nil
	}}
	if err := runtime.OpenComposer(context.Background(), workspace); err != nil {
		t.Fatalf("OpenComposer() error = %v", err)
	}
	target, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "claude" || target.Host != "code" || target.Path != "/new" {
		t.Fatalf("route = %q, want claude://code/new", opened)
	}
	if len(target.Query()) != 1 || target.Query().Get("folder") != workspace {
		t.Fatalf("query = %q, want only exact folder %q", target.RawQuery, workspace)
	}
	if !runtime.deepLinkOpened {
		t.Fatal("successful official route did not arm provisional cleanup")
	}
}

func TestOpenComposerArmsCleanupBeforeTheExternalRouteCanFail(t *testing.T) {
	runtime := &LiveRuntime{openDeepLink: func(_ context.Context, _ string) error {
		return os.ErrPermission
	}}
	if err := runtime.OpenComposer(context.Background(), t.TempDir()); err == nil {
		t.Fatal("OpenComposer() returned nil error")
	}
	if !runtime.deepLinkOpened {
		t.Fatal("failed route did not leave provisional cleanup armed")
	}
}

func TestComposerDeadlineNamesObservedNoFolderWorkspaceMismatch(t *testing.T) {
	elements := archiveFixtureElements("No folder", "Unused")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/exact-workspace",
		func(context.Context) ([]helperElement, error) { return elements, nil },
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error {
			t.Fatal("no trust prompt is on screen; the wait must not click anything")
			return nil
		},
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "No folder") ||
		!strings.Contains(err.Error(), "project") || !strings.Contains(err.Error(), "/repo/exact-workspace") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
}

// trustPromptElements builds the exact two-button shape Claude Desktop shows
// for an untrusted workspace, optionally alongside a working composer.
func trustPromptElements(confirmTitle, cancelTitle string, withComposer bool) []helperElement {
	elements := []helperElement{
		{Path: []int{9, 0}, Role: "AXButton", Title: confirmTitle, Hierarchy: []string{"AXApplication", "AXWindow", "AXSheet"}},
		{Path: []int{9, 1}, Role: "AXButton", Title: cancelTitle, Hierarchy: []string{"AXApplication", "AXWindow", "AXSheet"}},
	}
	if withComposer {
		elements = append(elements, archiveFixtureElements("workspace", "Owned")...)
	}
	return elements
}

// The trust prompt stands in front of the composer on every desktop-local run,
// because the staging workspace is new each time. The wait must answer it and
// then go on to verify the composer.
func TestComposerWaitAnswersTheWorkspaceTrustPrompt(t *testing.T) {
	answered := 0
	inspections := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var steps []string
	controls, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			inspections++
			if answered == 0 {
				return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
			}
			return archiveFixtureElements("workspace", "Owned"), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(_ context.Context, selector helperSelector, post helperPostcondition) error {
			if selector.Title != trustConfirmTitle {
				t.Fatalf("clicked %q, want %q", selector.Title, trustConfirmTitle)
			}
			if post.Condition != "absent" {
				t.Fatalf("postcondition = %q, want the prompt to go away", post.Condition)
			}
			answered++
			return nil
		},
		func(step string) { steps = append(steps, step) },
	)
	if err != nil {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if answered != 1 {
		t.Fatalf("trust prompt answered %d times, want exactly 1", answered)
	}
	if len(controls) == 0 {
		t.Fatal("the composer was never verified after the trust prompt")
	}
	if len(steps) != 1 || steps[0] != "trust-workspace-prompt-answered" {
		t.Fatalf("steps = %v; the trust grant must be recorded in the run log", steps)
	}
}

// A prompt that keeps coming back is not this run's own scratch folder being
// trusted, and the tree does not carry the folder being asked about — so the
// second one is a stop, never another click.
func TestComposerWaitRefusesASecondTrustPrompt(t *testing.T) {
	clicks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error { clicks++; return nil },
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "second workspace trust prompt") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if clicks != 1 {
		t.Fatalf("clicked %d times, want exactly 1", clicks)
	}
}

// Only the exact two-button shape is a trust prompt. A lone confirm-looking
// button must never be clicked.
func TestTrustPromptRequiresBothButtons(t *testing.T) {
	for _, shape := range []struct {
		name    string
		confirm string
		cancel  string
	}{
		{"no cancel button", trustConfirmTitle, "Something else"},
		{"no confirm button", "Allow", trustCancelTitle},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, prompted := trustPromptButton(
				trustPromptElements(shape.confirm, shape.cancel, false)); prompted {
				t.Fatal("a non-matching shape was read as a trust prompt")
			}
		})
	}
	if _, prompted := trustPromptButton(trustPromptElements(trustConfirmTitle, trustCancelTitle, false)); !prompted {
		t.Fatal("the exact trust-prompt shape was not recognised")
	}
}

// The trust sheet animates in, so the helper's own hit test can refuse the
// first click. That must be retried, and must not spend the once-only guard.
func TestComposerWaitRetriesATrustClickThatMissedTheAnimatingSheet(t *testing.T) {
	attempts := 0
	var steps []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controls, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			if attempts < 2 {
				return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
			}
			return archiveFixtureElements("workspace", "Owned"), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error {
			attempts++
			if attempts == 1 {
				return errors.New("helper stale_control: The current click point does not hit the selected control.")
			}
			return nil
		},
		func(step string) { steps = append(steps, step) },
	)
	if err != nil {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("click attempts = %d, want 2 (one missed, one landed)", attempts)
	}
	if len(controls) == 0 {
		t.Fatal("the composer was never verified after the retried trust click")
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %v; only the CONFIRMED grant may be recorded", steps)
	}
}

// A click failure that is not the animation race is a stop.
func TestComposerWaitFailsOnANonTransientTrustClickError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error {
			return errors.New("helper permission_denied: Accessibility is not trusted")
		},
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "answer Desktop workspace trust prompt") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
}

// Claude Desktop re-renders the composer right after the trust sheet closes,
// and the helper's tree walk fails whole when a node goes away underneath it.
// That is a reason to look again; the poll deadline is what still fails loudly.
func TestComposerWaitRetriesAStaleAccessibilityRead(t *testing.T) {
	reads := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controls, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			reads++
			if reads == 1 {
				return nil, errors.New(
					"helper action_failed: Accessibility could not read AXRole (" + axInvalidUIElement + ").")
			}
			return archiveFixtureElements("workspace", "Owned"), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error {
			t.Fatal("no trust prompt is on screen; the wait must not click anything")
			return nil
		},
		func(string) {},
	)
	if err != nil {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if reads != 2 {
		t.Fatalf("inspect calls = %d, want 2 (one stale, one settled)", reads)
	}
	if len(controls) == 0 {
		t.Fatal("the composer was never verified after the stale read")
	}
}

// A different action_failed is still a stop: only the vanished-element code is
// a retry.
func TestComposerWaitStopsOnANonStaleHelperFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			return nil, errors.New("helper action_failed: Accessibility refused the text value update (AX error -25200).")
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error { return nil },
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "AX error -25200") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
}
