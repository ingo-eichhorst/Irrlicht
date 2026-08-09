// disclosure.go renders the event list an adapter's consent copy shows the
// user (issue #1356). The wizard's Touches/Detail text is the consent contract
// for a modify-kind permission — it is what the user reads before granting —
// so it has to name what the installer actually writes. Restating the list by
// hand is what let Claude Code's copy sit at six events for the whole of
// #1173's seven-event install; deriving it from the same slice the installer
// consumes makes that drift unrepresentable rather than merely tested for.
package hookjson

import "strings"

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
