package desktopdriver

import (
	"context"
	"fmt"
)

// The measured slash-command sequence on Claude Desktop 1.46388.4, 2026-09-07.
// Trees at replaydata/agents/claudecode/desktop-evidence/, write-up in
// slash-commands.md.
//
//	type "/<name>"   -> the command popup opens and filters
//	Return           -> the highlighted entry is accepted
//	type "<args>"    -> arguments land after the accepted command
//	click Send       -> Claude Desktop RUNS the command
//
// The app then heads the message "You said: /<name> <args>", which is how the
// final step was proven to run the command rather than send its text.

// acceptedComposerValue is what the composer reads once the popup has accepted
// a command. It is a MEASURED shape, not a derived one, and both forms were
// measured twice — on /compact and on /model:
//
//	accepted, no arguments  -> "compact "        (name, one space)
//	accepted, "hello world" -> "compact\n hello world"
//
// The space after the name becomes a newline plus a space as soon as anything
// follows it. That is the app's own serialization of a command chip followed by
// text, and pinning it here is what makes each typing step fail loudly if a
// future build changes the shape.
func acceptedComposerValue(name, args string) string {
	if args == "" {
		return name + " "
	}
	return name + "\n " + args
}

// composeSlashCommand types a slash command and accepts it from the popup,
// leaving the composer ready for Submit.
//
// NOTHING here is retried once typing has begun. Typing is not idempotent: a
// second attempt appends rather than replaces, so a retry would turn "/compact"
// into "/compact/compact" and the postcondition would then be failing for a
// reason the retry itself created. The read-only prologue — activate, inspect,
// prove the owned conversation, resolve the composer — is where a moving
// accessibility tree is tolerated, and it is the caller's retry that covers it.
//
// The composer must be EMPTY when this runs, and nothing here clears it. That
// is deliberate: `set_value ""` cannot verify itself, because an empty Desktop
// composer reads back as its own placeholder text rather than as "". The first
// postcondition does the job instead — text left over from an earlier step
// makes the composer read "<leftover>/compact" and the step fails by name.
func composeSlashCommand(
	ctx context.Context,
	workspace string,
	owned OwnedSession,
	text string,
	activate func(context.Context) error,
	inspect func(context.Context) ([]helperElement, error),
	typeText func(context.Context, helperSelector, string, helperPostcondition) error,
	keyboard func(context.Context, helperSelector, uint16, []string, helperPostcondition) error,
) error {
	name, args, err := splitSlashCommand(text)
	if err != nil {
		return err
	}
	// Checked again here, not only in the planner: composeSlashCommand is the
	// last place that can refuse before a keystroke lands, and a caller that
	// reaches it without planning must not type text the composer would rewrite.
	if err := requireTypableSlashArguments(args); err != nil {
		return fmt.Errorf("/%s: %w", name, err)
	}
	var prompt helperSelector
	if err := retryTransientAXFor(ctx, "resolve the Desktop composer", submitAttempts, func() error {
		if err := activate(ctx); err != nil {
			return err
		}
		elements, err := inspect(ctx)
		if err != nil {
			return err
		}
		if err := requireOwnedConversationOpen(elements, owned); err != nil {
			return err
		}
		controls, err := composerControls(elements, workspace, []string{controlPrompt})
		if err != nil {
			return err
		}
		prompt = controls[controlPrompt]
		return nil
	}); err != nil {
		return err
	}

	typed := "/" + name
	if err := typeText(ctx, prompt, typed, valueEquals(prompt, typed)); err != nil {
		return fmt.Errorf("type %q into the Desktop composer: %w", typed, err)
	}
	elements, err := inspect(ctx)
	if err != nil {
		return fmt.Errorf("read the Desktop command popup: %w", err)
	}
	if err := requireSlashPopupOffers(elements, name); err != nil {
		return err
	}
	accepted := acceptedComposerValue(name, "")
	if err := keyboard(ctx, prompt, slashAcceptKeyCode, nil, valueEquals(prompt, accepted)); err != nil {
		return fmt.Errorf("accept the Desktop command %q from its popup: %w", name, err)
	}
	if args == "" {
		return nil
	}
	withArgs := acceptedComposerValue(name, args)
	if err := typeText(ctx, prompt, args, valueEquals(prompt, withArgs)); err != nil {
		return fmt.Errorf("type the arguments for %q: %w", "/"+name, err)
	}
	return nil
}

// slashAcceptKeyCode is kVK_Return. It is spelled out here rather than taken
// from desktopKeys because that table is the recipe grammar's `keys` step,
// whose entries are named by what a RECIPE may ask for. This Return is internal
// to the slash sequence and carries a different postcondition.
const slashAcceptKeyCode uint16 = 36

func valueEquals(selector helperSelector, value string) helperPostcondition {
	return helperPostcondition{
		Selector:            selector,
		Condition:           "value_equals",
		Value:               value,
		TimeoutMilliseconds: 8_000,
	}
}
