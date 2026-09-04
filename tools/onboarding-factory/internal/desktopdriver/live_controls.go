package desktopdriver

// The composer controls the recipe grammar drives beyond prompt-and-send: stop,
// raw keyboard, and the mode and model popup menus.
//
// Every selector here comes from the verified composer catalog (catalog.go),
// which is itself pinned to the measured accessibility dump. Nothing in this
// file invents a path. `stop` is derived from the measured Send button the same
// way Submit's postcondition already derives it: Claude Desktop replaces Send
// with a Stop button in the same row while a turn is in flight.

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

// stopSelector is the in-flight state of the composer's Send button. Desktop
// swaps the button in place, so it carries Send's measured hierarchy.
func (runtime *LiveRuntime) stopSelector() (helperSelector, error) {
	send, err := runtime.control(controlSend)
	if err != nil {
		return helperSelector{}, err
	}
	return helperSelector{Role: "AXButton", Description: "Stop", Hierarchy: send.Hierarchy}, nil
}

// Interrupt clicks the composer's Stop button and proves the click landed by
// waiting for Send to come back. A postcondition on Stop's own absence would
// also pass if the whole composer went away.
func (runtime *LiveRuntime) Interrupt(ctx context.Context) error {
	send, err := runtime.control(controlSend)
	if err != nil {
		return err
	}
	stop, err := runtime.stopSelector()
	if err != nil {
		return err
	}
	return runtime.helper.click(ctx, stop, helperPostcondition{
		Selector: send, Condition: "exists", TimeoutMilliseconds: popupCloseTimeout,
	})
}

// PressKey sends one raw keystroke to a composer control, with the
// false-to-true postcondition the helper requires. A key with no such
// postcondition is refused by Plan long before this runs; the check is repeated
// here because this is the last place that can still refuse.
func (runtime *LiveRuntime) PressKey(ctx context.Context, key string) error {
	definition, ok := desktopKeys[key]
	if !ok {
		return fmt.Errorf("key %q has no observable Desktop postcondition; supported keys are %s",
			key, strings.Join(SupportedKeys(), ", "))
	}
	send, err := runtime.control(controlSend)
	if err != nil {
		return err
	}
	stop, err := runtime.stopSelector()
	if err != nil {
		return err
	}
	prompt, err := runtime.control(controlPrompt)
	if err != nil {
		return err
	}
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
	if err := runtime.helper.keyboard(ctx, prompt, definition.code, nil, after); err != nil {
		return fmt.Errorf("press %s (expected %s): %w", key, definition.effect, err)
	}
	return nil
}

// SelectMode and SelectModel drive the two composer popup menus. Both use the
// same two-click shape the archive path uses: open the popup, prove a menu
// appeared, resolve exactly one menu item by title, click it, and prove the
// menu closed AND the popup now reports the requested entry.
func (runtime *LiveRuntime) SelectMode(ctx context.Context, value string) error {
	return runtime.selectFromPopup(ctx, controlMode, value, modeReportsEntry)
}

func (runtime *LiveRuntime) SelectModel(ctx context.Context, value string) error {
	return runtime.selectFromPopup(ctx, controlModel, value, modelReportsEntry)
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

func (runtime *LiveRuntime) selectFromPopup(
	ctx context.Context,
	control string,
	value string,
	reports func(helperElement, string) bool,
) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("Desktop %s selection needs a menu entry name", control)
	}
	popup, err := runtime.control(control)
	if err != nil {
		return err
	}
	menuRole := helperSelector{Role: "AXMenu"}
	if err := runtime.helper.click(ctx, popup, helperPostcondition{
		Selector: menuRole, Condition: "exists", TimeoutMilliseconds: popupOpenTimeout,
	}); err != nil {
		return fmt.Errorf("open Desktop %s popup: %w", control, err)
	}
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return fmt.Errorf("read Desktop %s menu: %w", control, err)
	}
	entry, err := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXMenuItem" && strings.EqualFold(strings.TrimSpace(element.Title), strings.TrimSpace(value))
	}, fmt.Sprintf("Desktop %s menu entry %q", control, value))
	if err != nil {
		return err
	}
	if err := runtime.helper.click(ctx, selectorFor(entry), helperPostcondition{
		Selector: menuRole, Condition: "absent", TimeoutMilliseconds: popupCloseTimeout,
	}); err != nil {
		return fmt.Errorf("select Desktop %s entry %q: %w", control, value, err)
	}
	// A closed menu is not a changed setting. Read the popup back and require it
	// to announce the entry that was asked for — this is the only observation
	// that separates "the click landed" from "the click did something".
	return poll(ctx, fmt.Sprintf("Desktop %s popup reporting %q", control, value), func() (bool, error) {
		current, err := runtime.helper.inspect(ctx)
		if err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			return false, nil
		}
		element, ok := elementAtPath(current, composerPaths[control])
		if !ok {
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
