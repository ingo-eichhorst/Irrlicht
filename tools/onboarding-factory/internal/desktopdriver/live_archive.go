package desktopdriver

// Archiving the owned Desktop session, and the guards that keep the driver
// from archiving one it did not create.

import (
	"context"
	"fmt"
)

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
	menuRole := helperSelector{Role: "AXMenu"}
	if err := runtime.helper.click(ctx, target.menu, helperPostcondition{
		Selector: menuRole, Condition: "exists", TimeoutMilliseconds: 2_000,
	}); err != nil {
		return fmt.Errorf("open owned-session menu: %w", err)
	}
	elements, err = runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	archive, err := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXMenuItem" && element.Title == "Archive"
	}, "Archive menu item")
	if err != nil {
		return err
	}
	if err := runtime.helper.click(ctx, selectorFor(archive), helperPostcondition{
		Selector: menuRole, Condition: "absent", TimeoutMilliseconds: 10_000,
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
	titleMatches := 0
	for _, session := range sessions {
		if !session.Archived && session.Title == registry.Title {
			titleMatches++
		}
	}
	if titleMatches != 1 {
		return archiveTarget{}, fmt.Errorf("owned active session title %q is not unique; found %d rows", registry.Title, titleMatches)
	}
	// The composer is gone by now: after a turn Claude Desktop shows the
	// session, not a fresh composer. The ownership binding that survives is the
	// selected-session menu, which names the owned title verified unique above.
	menu, err := selectedSessionMenu(elements, registry.Title)
	if err != nil {
		return archiveTarget{}, err
	}
	return archiveTarget{registry: registry, menu: menu}, nil
}
