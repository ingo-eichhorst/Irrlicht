package desktopdriver

// The composer controls the recipe grammar drives beyond prompt-and-send: stop,
// raw keyboard, and the mode and model popup menus.
//
// Every selector here comes from the verified composer catalog (catalog.go),
// which is itself pinned to the measured accessibility dump. Nothing in this
// file invents a path. `stop` is derived from the measured Send button the same
// way Submit's postcondition already derives it: Claude Desktop replaces Send
// with a Stop button in the same row while a turn is in flight.
//
// Every LiveRuntime method below is a thin wrapper around a free function
// parameterized by `inspect`/`click`/`keyboard` closures — the same seam
// waitForComposerControls (live.go) already uses for the composer wait — so
// each one is testable against a fake accessibility tree without a live
// helper subprocess. live_controls_test.go exercises all four this way.
//
// Every action here RESOLVES a control and then DRIVES it — exactly the shape
// live.go's retryTransientAX exists for: Desktop's renderer can swap the
// composer out between the resolve and the click, and re-using a selector
// resolved before that is what fails with a stale control. Each resolve+drive
// step below runs inside its own retryTransientAX call, re-resolving fresh on
// every attempt, the same way Submit and ArchiveOwned already do.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// popupSettleTimeout bounds every postcondition on this path. It is the same
// budget the archive path uses for opening and closing a menu.
const (
	popupOpenTimeout  = 2_000
	popupCloseTimeout = 10_000
)

func (runtime *LiveRuntime) control(name string) (helperSelector, error) {
	selector, ok := runtime.controls[name]
	if !ok {
		return helperSelector{}, fmt.Errorf("Desktop %s control was not verified", name)
	}
	return selector, nil
}

// freshControls re-inspects the live Desktop tree and resolves exactly the
// named controls by identity. It exists because WaitComposer only ever caches
// basicTurnControls (environment, project, prompt) into runtime.controls — see
// its comment in catalog.go — so `send`, `mode`, and `model` are never in that
// cache and must resolve themselves fresh on every use.
func freshControls(
	ctx context.Context,
	workspace string,
	names []string,
	inspect func(context.Context) ([]helperElement, error),
) (map[string]helperSelector, error) {
	elements, err := inspect(ctx)
	if err != nil {
		return nil, err
	}
	return composerControls(elements, workspace, names)
}

// freshSendAndStop resolves the composer's Send button and its in-flight Stop
// state from a FRESH reading, never from the controls WaitComposer verified.
// `send` is not among basicTurnControls, and the slot is state dependent
// besides — the SAME slot reads "Send" or "Stop" depending on whether a turn
// is running, so resolving it any earlier than the moment of use could
// observe either label. Stop itself carries no separate identity: it is
// Send's measured hierarchy with Desktop's in-flight description swapped in.
func freshSendAndStop(
	ctx context.Context,
	workspace string,
	inspect func(context.Context) ([]helperElement, error),
) (send, stop helperSelector, err error) {
	controls, err := freshControls(ctx, workspace, []string{controlSend}, inspect)
	if err != nil {
		return helperSelector{}, helperSelector{}, err
	}
	send = controls[controlSend]
	stop = stopSelectorFor(send)
	return send, stop, nil
}

// stopButtonDescription is the label Claude Desktop puts on the send slot while
// a turn is in flight.
const stopButtonDescription = "Stop"

func stopSelectorFor(send helperSelector) helperSelector {
	return helperSelector{Role: "AXButton", Description: stopButtonDescription, Hierarchy: send.Hierarchy}
}

// turnInFlight reports whether Claude Desktop is running a turn right now.
//
// Measured on 1.46388.4 on 2026-09-06 against the live app: an idle window
// exposes one AXButton described "Send" and NO button described "Stop" — and
// that held with another session shown as "Running" in the sidebar, which
// contributes a row TITLE and no button of its own. So a Stop button is the
// composer's in-flight face and nothing else's, which is what lets Submit read
// it as proof that a click of its own already landed.
func turnInFlight(elements []helperElement) bool {
	for _, element := range elements {
		if element.Role == "AXButton" && element.Description == stopButtonDescription {
			return true
		}
	}
	return false
}

// Interrupt clicks the composer's Stop button and proves the click landed by
// waiting for Send to come back. A postcondition on Stop's own absence would
// also pass if the whole composer went away.
func (runtime *LiveRuntime) Interrupt(ctx context.Context) error {
	return interruptTurn(ctx, runtime.workspace, runtime.helper.inspect, runtime.helper.click)
}

func interruptTurn(
	ctx context.Context,
	workspace string,
	inspect func(context.Context) ([]helperElement, error),
	click func(context.Context, helperSelector, helperPostcondition) error,
) error {
	return retryTransientAX(ctx, "interrupt the in-flight Desktop turn", func() error {
		send, stop, err := freshSendAndStop(ctx, workspace, inspect)
		if err != nil {
			return err
		}
		return click(ctx, stop, helperPostcondition{
			Selector: send, Condition: "exists", TimeoutMilliseconds: popupCloseTimeout,
		})
	})
}

// PressKey sends one raw keystroke to a composer control, with the
// false-to-true postcondition the helper requires. A key with no such
// postcondition is refused by Plan long before this runs; the check is repeated
// here because this is the last place that can still refuse.
func (runtime *LiveRuntime) PressKey(ctx context.Context, key string) error {
	return pressKey(ctx, key, runtime.workspace, runtime.helper.inspect, runtime.helper.keyboard)
}

func pressKey(
	ctx context.Context,
	key string,
	workspace string,
	inspect func(context.Context) ([]helperElement, error),
	keyboard func(context.Context, helperSelector, uint16, []string, helperPostcondition) error,
) error {
	definition, ok := desktopKeys[key]
	if !ok {
		return fmt.Errorf("key %q has no observable Desktop postcondition; supported keys are %s",
			key, strings.Join(SupportedKeys(), ", "))
	}
	return retryTransientAX(ctx, fmt.Sprintf("press %s", key), func() error {
		controls, err := freshControls(ctx, workspace, []string{controlSend, controlPrompt}, inspect)
		if err != nil {
			return err
		}
		send := controls[controlSend]
		stop := helperSelector{Role: "AXButton", Description: "Stop", Hierarchy: send.Hierarchy}
		prompt := controls[controlPrompt]
		// Escape cancels an in-flight turn: Stop must give way to Send.
		// Enter submits: Send must give way to Stop.
		after := helperPostcondition{
			Selector: send, Condition: "exists", TimeoutMilliseconds: popupCloseTimeout,
		}
		if key == "Enter" {
			after = helperPostcondition{
				Selector: stop, Condition: "exists", TimeoutMilliseconds: popupCloseTimeout,
			}
		}
		if err := keyboard(ctx, prompt, definition.code, nil, after); err != nil {
			return fmt.Errorf("press %s (expected %s): %w", key, definition.effect, err)
		}
		return nil
	})
}

// SelectMode and SelectModel drive the two composer popup menus. Both use the
// same two-click shape the archive path uses: open the popup, prove a menu
// appeared, resolve exactly one menu item by title, click it, and prove the
// menu closed AND the popup now reports the requested entry.
func (runtime *LiveRuntime) SelectMode(ctx context.Context, value string) error {
	return selectFromPopup(ctx, runtime.workspace, controlMode, value, modeReportsEntry,
		runtime.helper.inspect, runtime.helper.click)
}

func (runtime *LiveRuntime) SelectModel(ctx context.Context, value string) error {
	return selectFromPopup(ctx, runtime.workspace, controlModel, value, modelReportsEntry,
		runtime.helper.inspect, runtime.helper.click)
}

// modeReportsEntry and modelReportsEntry say how each popup announces its
// current entry. The measured tree carries the mode in the popup's TITLE
// ("Auto") and the model in its DESCRIPTION ("Model: Opus 5"), so the two
// cannot share one check.
func modeReportsEntry(element helperElement, value string) bool {
	return strings.EqualFold(strings.TrimSpace(element.Title), strings.TrimSpace(value))
}

func modelReportsEntry(element helperElement, value string) bool {
	prefix := "Model: "
	if !strings.HasPrefix(element.Description, prefix) {
		return false
	}
	return strings.EqualFold(
		strings.TrimSpace(strings.TrimPrefix(element.Description, prefix)),
		strings.TrimSpace(value))
}

func selectFromPopup(
	ctx context.Context,
	workspace string,
	control string,
	value string,
	reports func(helperElement, string) bool,
	inspect func(context.Context) ([]helperElement, error),
	click func(context.Context, helperSelector, helperPostcondition) error,
) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("Desktop %s selection needs a menu entry name", control)
	}
	menuRole := helperSelector{Role: "AXMenu"}
	if err := retryTransientAX(ctx, fmt.Sprintf("open Desktop %s popup", control), func() error {
		controls, err := freshControls(ctx, workspace, []string{control}, inspect)
		if err != nil {
			return err
		}
		return click(ctx, controls[control], helperPostcondition{
			Selector: menuRole, Condition: "exists", TimeoutMilliseconds: popupOpenTimeout,
		})
	}); err != nil {
		return fmt.Errorf("open Desktop %s popup: %w", control, err)
	}
	// Re-read the menu and click inside the retry: the item animates in, and a
	// selector resolved before it settled is what refuses the click — the same
	// shape ArchiveOwned already retries for its own menu item.
	if err := retryTransientAX(ctx, fmt.Sprintf("select Desktop %s entry %q", control, value), func() error {
		elements, err := inspect(ctx)
		if err != nil {
			return err
		}
		entry, err := uniqueElement(elements, func(element helperElement) bool {
			return element.Role == "AXMenuItem" && strings.EqualFold(strings.TrimSpace(element.Title), strings.TrimSpace(value))
		}, fmt.Sprintf("Desktop %s menu entry %q", control, value))
		if err != nil {
			return err
		}
		return click(ctx, selectorFor(entry), helperPostcondition{
			Selector: menuRole, Condition: "absent", TimeoutMilliseconds: popupCloseTimeout,
		})
	}); err != nil {
		return fmt.Errorf("select Desktop %s entry %q: %w", control, value, err)
	}
	// A closed menu is not a changed setting. Read the popup back and require it
	// to announce the entry that was asked for — this is the only observation
	// that separates "the click landed" from "the click did something".
	return poll(ctx, fmt.Sprintf("Desktop %s popup reporting %q", control, value), func() (bool, error) {
		current, err := inspect(ctx)
		if err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			return false, nil
		}
		element, err := matchedElement(current, workspace, control)
		if err != nil {
			return false, nil
		}
		return reports(element, value), nil
	})
}

// Sleep is the recipe's `sleep` step. It honours the run deadline, and refuses a
// duration outside the range Plan already bounded — a negative or oversized
// sleep reaching here means the plan and the executor disagree.
func (runtime *LiveRuntime) Sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 || duration > maxRecipeSleepSeconds*time.Second {
		return fmt.Errorf("a recipe sleep must be 0s < d <= %ds; got %s", maxRecipeSleepSeconds, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("Desktop recipe sleep was cut short"), ctx.Err())
	}
}
