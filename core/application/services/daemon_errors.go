package services

import (
	"fmt"
	"sort"
)

// Daemon-wide error kinds. The client keys its banner text off Message, not
// off Kind — these exist so a reader (and a test) can tell the two diagnoses
// apart without string-matching prose.
const (
	// DaemonErrorHookEntriesMissing is #1372's diagnosis: our hook entries
	// were installed and have since gone from the agent's config, or the
	// config could not be read back at all.
	DaemonErrorHookEntriesMissing = "hook_entries_missing"
	// DaemonErrorHookChannelSilent is #1368's diagnosis: the entries are
	// present, consent is granted, and yet nothing is arriving — so that
	// adapter's sessions have been demoted to transcript-tier classification.
	DaemonErrorHookChannelSilent = "hook_channel_silent"
)

// DaemonError is one fault in Irrlicht's OWN machinery that has no session to
// attach to.
//
// This is the daemon-wide half of #1796's error work, and it is a different
// thing from session.SessionError. A session error says "the agent's provider
// call failed" and turns one row red. A DaemonError says "Irrlicht is not
// watching properly", which invalidates what the user is reading everywhere at
// once — sessions may be silently misclassified, and every one of them will
// look fine.
//
// NOTHING IS INVENTED HERE. Both kinds are diagnoses the daemon already
// computes on a timer and already writes into the diagnostics bundle's
// hooks.json; until now that was their only outlet, so a fault that degrades
// every session's classification was visible exclusively to someone who
// thought to run `--diagnose` and unpack a tarball. This type is a second
// outlet for the same facts, not a second detector.
//
// It deliberately does NOT fold in #1362/#1365 (a hook install that FAILED, or
// was refused below a version floor). Those already reach the client as
// PermissionsSnapshot.UnappliedGrants and already have their own banner, and
// permission_service.go's scope comment locks the three diagnoses apart on
// purpose: an install that never wrote entries, entries that were written and
// vanished, and entries that are present but dead are three different things
// to go fix.
type DaemonError struct {
	// Kind is the machine-readable diagnosis; see the constants above.
	Kind string `json:"kind"`
	// Scope names what the fault is about — the adapter, plus the permission
	// key where one target among several is affected. A fault with no scope
	// would not be actionable.
	Scope string `json:"scope"`
	// Message is the one sentence a user reads. Composed daemon-side so the
	// two frontends cannot word the same fault differently (the convention
	// cache_bloat_explanation already follows).
	Message string `json:"message"`
	// Detail is the machine's own words — a config path, the verifier's last
	// error — carried verbatim rather than summarised, and empty when there is
	// nothing to add.
	Detail string `json:"detail,omitempty"`
}

// DaemonErrors derives the client-facing daemon-wide fault list from a hook
// health snapshot.
//
// A PURE FUNCTION over the snapshot, so it is testable without a daemon and so
// the shape on the wire cannot drift from the shape in hooks.json. It returns
// nil (not an empty slice) when everything is healthy, which is what makes the
// `omitempty` tag on the response field honest: a healthy daemon's payload is
// byte-identical to what it was before this field existed.
//
// Both loops skip rows that are not being watched. An unwatched row means
// consent was never granted or no pass has run yet — that is a user decision
// or a cold start, not a fault, and reporting it would train the banner to be
// ignored.
func DaemonErrors(h HookHealthSnapshot) []DaemonError {
	var out []DaemonError

	for _, t := range h.EntryReverification.Targets {
		if !t.Watched {
			continue
		}
		switch {
		case t.LastError != "":
			out = append(out, DaemonError{
				Kind:    DaemonErrorHookEntriesMissing,
				Scope:   t.Adapter + "/" + t.Permission,
				Message: fmt.Sprintf("Irrlicht could not keep its %s hooks installed — %s sessions may be misclassified.", t.Adapter, t.Adapter),
				Detail:  detailWithPath(t.LastError, t.ConfigPath),
			})
		case len(t.Missing) > 0:
			out = append(out, DaemonError{
				Kind:    DaemonErrorHookEntriesMissing,
				Scope:   t.Adapter + "/" + t.Permission,
				Message: fmt.Sprintf("Irrlicht's %s hook entries went missing from the agent's config — %s sessions may be misclassified.", t.Adapter, t.Adapter),
				Detail:  detailWithPath(joinShort(t.Missing), t.ConfigPath),
			})
		}
	}

	for _, c := range h.Channels {
		// Armed means consent is granted AND the install reported success, so
		// a silent armed channel is the case where everything LOOKS configured
		// and nothing is arriving — the one #1368 exists for.
		if !c.Armed || !c.Silent {
			continue
		}
		out = append(out, DaemonError{
			Kind:    DaemonErrorHookChannelSilent,
			Scope:   c.Adapter,
			Message: fmt.Sprintf("Irrlicht is receiving no hook events from %s — its sessions have fallen back to slower transcript-only detection.", c.Adapter),
			Detail:  fmt.Sprintf("%d completed turns with no receipt", c.TurnsSinceReceipt),
		})
	}

	// Deterministic order. The client's banner skips a re-render when the
	// content is unchanged (the dataset.bannerKey idiom), and map iteration
	// upstream would otherwise make an unchanged fault list look different on
	// every poll — re-announcing the same fault to a screen reader forever.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// detailWithPath appends the config path to a detail string when there is one,
// because "entries missing" without the file to go look at is not actionable —
// the same reasoning HookEntryHealth.ConfigPath's own doc gives.
func detailWithPath(detail, path string) string {
	switch {
	case detail == "" && path == "":
		return ""
	case path == "":
		return detail
	case detail == "":
		return path
	}
	return detail + " (" + path + ")"
}

// joinShort renders a missing-entry list as prose, capped so one pathological
// config cannot push a banner past a readable length.
func joinShort(items []string) string {
	const max = 3
	if len(items) <= max {
		return joinComma(items)
	}
	return fmt.Sprintf("%s and %d more", joinComma(items[:max]), len(items)-max)
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
