package desktopdriver

// Archiving the owned Desktop session, and the guards that keep the driver
// from archiving one it did not create.

import (
	"context"
	"fmt"
)

const archiveMenuItemTitle = "Archive"

func (runtime *LiveRuntime) ArchiveOwned(ctx context.Context, owned OwnedSession) error {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return err
	}
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	target, err := validateArchiveTarget(owned, sessions, elements)
	if err != nil {
		return err
	}
	if target.registry.Archived {
		return nil
	}
	// Re-probe the control about to be clicked, not a neighbour of it: a
	// freshness check on anything else proves nothing about this click.
	if err := runtime.helper.probeSelector(ctx, "owned-session menu", target.menu); err != nil {
		return fmt.Errorf("re-probe owned-session menu before archive: %w", err)
	}
	// Watch for the Archive item itself, not for "a menu". Claude Desktop has a
	// menu bar, so `AXMenu` is never unique — live run 19 archived nothing and
	// reported `The postcondition selector matched 16 visible controls`. The
	// item this click exists to reveal is both unique and the thing needed next.
	archiveItem := helperSelector{Role: "AXMenuItem", Title: archiveMenuItemTitle}
	if err := runtime.helper.click(ctx, target.menu, helperPostcondition{
		Selector: archiveItem, Condition: "exists", TimeoutMilliseconds: 5_000,
	}); err != nil {
		return fmt.Errorf("open owned-session menu: %w", err)
	}
	elements, err = runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	archive, err := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXMenuItem" && element.Title == archiveMenuItemTitle
	}, "Archive menu item")
	if err != nil {
		return err
	}
	// And watch for that same item to go away, for the same reason.
	if err := runtime.helper.click(ctx, selectorFor(archive), helperPostcondition{
		Selector: archiveItem, Condition: "absent", TimeoutMilliseconds: 10_000,
	}); err != nil {
		return fmt.Errorf("archive owned session: %w", err)
	}
	return poll(ctx, "owned registry archive flag", func() (bool, error) {
		current, err := runtime.registrySession(owned.Registry.SessionID)
		return err == nil && current.Archived, err
	})
}

func validateArchiveTarget(
	owned OwnedSession,
	sessions []RegistrySession,
	elements []helperElement,
) (archiveTarget, error) {
	var matches []RegistrySession
	for _, session := range sessions {
		if session.SessionID == owned.Registry.SessionID {
			matches = append(matches, session)
		}
	}
	if len(matches) != 1 {
		return archiveTarget{}, fmt.Errorf(
			"Desktop registry session %q requires one row; found %d",
			owned.Registry.SessionID,
			len(matches),
		)
	}
	registry := matches[0]
	if err := validateRegistryIdentity(owned.Registry, registry); err != nil {
		return archiveTarget{}, err
	}
	if registry.Archived {
		return archiveTarget{registry: registry}, nil
	}
	if registry.Title == "" {
		return archiveTarget{}, fmt.Errorf("owned session %q has no title for the selected-session guard", registry.SessionID)
	}
	// The owned title does NOT have to be unique among active sessions.
	// It used to, because the menu was found by title alone, and that made
	// every repeat run of one scenario unable to clean up after itself:
	// Desktop names a session after its content, so the same prompt earns the
	// same name every time. Three sessions on the development machine were
	// called "Confirmation response" before this was noticed.
	//
	// selectedSessionMenu now identifies the conversation that is OPEN, by its
	// nesting rather than its name, and then checks that it is this one. That
	// is the guarantee the uniqueness check was standing in for, and it is the
	// stronger of the two: the open conversation is the session the driver just
	// drove.
	// The composer is gone by now: after a turn Claude Desktop shows the
	// session, not a fresh composer. The ownership binding that survives is the
	// selected-session menu, which names the owned title verified unique above.
	menu, err := selectedSessionMenu(elements, registry.Title)
	if err != nil {
		return archiveTarget{}, err
	}
	return archiveTarget{registry: registry, menu: menu}, nil
}
