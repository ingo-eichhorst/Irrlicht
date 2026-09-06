package desktopdriver

// Archiving the owned Desktop session, and the guards that keep the driver
// from archiving one it did not create.

import (
	"context"
	"fmt"
)

const archiveMenuItemTitle = "Archive"

func (runtime *LiveRuntime) ArchiveOwned(ctx context.Context, owned OwnedSession) error {
	archived, err := runtime.ownedSessionAlreadyArchived(owned)
	if err != nil || archived {
		return err
	}
	// Watch for the Archive item itself, not for "a menu". Claude Desktop has a
	// menu bar, so `AXMenu` is never unique — live run 19 archived nothing and
	// reported `The postcondition selector matched 16 visible controls`. The
	// item this click exists to reveal is both unique and the thing needed next.
	archiveItem := helperSelector{Role: "AXMenuItem", Title: archiveMenuItemTitle}
	// Front and RE-RESOLVE on every attempt. This used to resolve the menu once,
	// outside the retry, and then re-click that one selector five times: cell
	// 2-18 failed all five with `stale_control: The current click point does not
	// hit the selected control`, which is the helper hit-testing a click point
	// the renderer had already moved. Re-clicking a selector resolved before the
	// move cannot succeed, however many times it is tried. Cleanup also runs
	// after the recipe is over, when anything on the machine can have taken
	// focus, and this path never brought Desktop forward at all.
	if err := retryTransientAX(ctx, "open the owned-session menu", func() error {
		if err := runtime.front(ctx); err != nil {
			return err
		}
		menu, archived, err := runtime.resolveArchiveMenu(ctx, owned)
		if err != nil || archived {
			return err
		}
		// Re-probe the control about to be clicked, not a neighbour of it: a
		// freshness check on anything else proves nothing about this click.
		if err := runtime.helper.probeSelector(ctx, "owned-session menu", menu); err != nil {
			return fmt.Errorf("re-probe owned-session menu before archive: %w", err)
		}
		return runtime.helper.click(ctx, menu, helperPostcondition{
			Selector: archiveItem, Condition: "exists", TimeoutMilliseconds: 5_000,
		})
	}); err != nil {
		return fmt.Errorf("open owned-session menu: %w", err)
	}
	if archived, err := runtime.ownedSessionAlreadyArchived(owned); err != nil || archived {
		return err
	}
	// Re-read the menu and click inside the retry: the item animates in, and a
	// selector resolved before it settled is what refuses the click.
	if err := retryTransientAX(ctx, "archive the owned Desktop session", func() error {
		elements, err := runtime.helper.inspect(ctx)
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
		return runtime.helper.click(ctx, selectorFor(archive), helperPostcondition{
			Selector: archiveItem, Condition: "absent", TimeoutMilliseconds: 10_000,
		})
	}); err != nil {
		return fmt.Errorf("archive owned session: %w", err)
	}
	return poll(ctx, "owned registry archive flag", func() (bool, error) {
		current, err := runtime.registrySession(owned.Registry.SessionID)
		return err == nil && current.Archived, err
	})
}

// ownedSessionAlreadyArchived reports whether Desktop's own registry already
// says this session is archived. A run that ends with its session archived —
// by an explicit `archive` step, or by a retry whose click landed after the
// helper stopped being able to verify it — has nothing left to do, and clicking
// again would open a menu on whatever now occupies that place.
func (runtime *LiveRuntime) ownedSessionAlreadyArchived(owned OwnedSession) (bool, error) {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return false, err
	}
	target, err := validateArchiveTargetRegistry(owned, sessions)
	if err != nil {
		return false, err
	}
	return target.Archived, nil
}

// resolveArchiveMenu re-reads BOTH sides of the ownership guard — Desktop's
// registry and its accessibility tree — and returns the menu to click. It
// reports archived=true when the registry says the work is already done, so a
// retry after a click that landed unverified stops instead of clicking twice.
func (runtime *LiveRuntime) resolveArchiveMenu(
	ctx context.Context,
	owned OwnedSession,
) (helperSelector, bool, error) {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return helperSelector{}, false, err
	}
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return helperSelector{}, false, err
	}
	target, err := validateArchiveTarget(owned, sessions, elements)
	if err != nil {
		return helperSelector{}, false, err
	}
	return target.menu, target.registry.Archived, nil
}

func validateArchiveTarget(
	owned OwnedSession,
	sessions []RegistrySession,
	elements []helperElement,
) (archiveTarget, error) {
	registry, err := validateArchiveTargetRegistry(owned, sessions)
	if err != nil {
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

// validateArchiveTargetRegistry is the registry half of the ownership guard,
// on its own so a caller that only needs to know whether the work is already
// done does not have to read the accessibility tree to find out.
func validateArchiveTargetRegistry(
	owned OwnedSession,
	sessions []RegistrySession,
) (RegistrySession, error) {
	var matches []RegistrySession
	for _, session := range sessions {
		if session.SessionID == owned.Registry.SessionID {
			matches = append(matches, session)
		}
	}
	if len(matches) != 1 {
		return RegistrySession{}, fmt.Errorf(
			"Desktop registry session %q requires one row; found %d",
			owned.Registry.SessionID,
			len(matches),
		)
	}
	registry := matches[0]
	if err := validateRegistryIdentity(owned.Registry, registry); err != nil {
		return RegistrySession{}, err
	}
	return registry, nil
}
