// disclosure.go renders the event list an adapter's consent copy shows the
// user (issue #1356). The wizard's Touches/Detail text is the consent contract
// for a modify-kind permission — it is what the user reads before granting —
// so it has to name what the installer actually writes. Restating the list by
// hand is what let Claude Code's copy sit at six events for the whole of
// #1173's seven-event install; deriving it from the same slice the installer
// consumes makes that drift unrepresentable rather than merely tested for.
package hookjson

import (
	"fmt"
	"strings"
)

// EntriesTouched renders the Touches line of a hooks permission: "Writes N
// hook entries to <path>". Both the number and the noun live here rather than
// in each adapter, so a third adapter cannot state a count that disagrees with
// its install, and cannot word the sentence in a way the contract assertion
// (which reads the count back out of the copy) fails to recognize.
func EntriesTouched(path string, events []string) string {
	return fmt.Sprintf("Writes %d hook entries to %s", len(events), path)
}

// EventList renders events as prose for consent copy: "A", "A and B",
// "A, B, and C". Callers pass the same slice they put in Config.Events, so the
// sentence and the install cannot disagree.
//
// The serial comma is deliberate and uniform across adapters: the contract
// assertion checks that every installed event is named, and a caller that
// hand-tuned the punctuation per adapter would be back to maintaining prose.
func EventList(events []string) string {
	switch len(events) {
	case 0:
		return ""
	case 1:
		return events[0]
	case 2:
		return events[0] + " and " + events[1]
	default:
		return strings.Join(events[:len(events)-1], ", ") + ", and " + events[len(events)-1]
	}
}

// RequiresVersion renders the sentence disclosing a hook install's declared
// minimum upstream CLI version (issue #1365).
//
// The floor belongs in the consent copy for the same reason the event list
// does. #1356's rule is that the text names what the installer writes; a
// version gate makes *whether* it writes conditional, so copy that describes
// the install without naming the condition is describing something that may
// not happen. Rendering it from the same string the gate compares against
// keeps the two from drifting — the failure #1356 exists to prevent, arriving
// on a new axis.
//
// The wording deliberately avoids CamelCase and any "N hook entries" phrasing:
// the disclosure contract scans the copy for event-shaped tokens and for entry
// counts, and a version sentence must not trip either.
func RequiresVersion(cliName, minVersion string) string {
	if minVersion == "" {
		return ""
	}
	return fmt.Sprintf(
		"Requires %s %s or newer; on an older version nothing is written and the daemon reports why.",
		cliName, minVersion)
}
