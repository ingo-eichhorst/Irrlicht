// herdrhint.go validates the herdr pane a pi session's own extension reports
// about itself (issue #1936).
//
// # Why the hint exists at all
//
// A session's launcher identity is normally read from OUTSIDE the process:
// processlifecycle.ReadLauncherEnv pulls a fixed whitelist of variables out of
// the agent's environment, which on darwin means sysctl(kern.procargs2). A
// Node-based agent that sets process.title overwrites the contiguous argv+env
// region that call exposes, so for pi the read comes back empty and the
// session gets no pane — measured in #1934 against herdr 0.8.0: `ps eww` shows
// zero variables for the pi process and a full environment for a compiled
// agent in a neighbouring pane.
//
// Inside the process the values are still there. irrlicht already ships an
// extension into pi (extension.js), so the one process that can read
// $HERDR_PANE_ID is already running irrlicht's code — it just was not looking.
//
// # Why it is confined rather than trusted
//
// The hint arrives on the same local, unauthenticated endpoint hooks.go's
// header already reasons about, and its conclusion applies here verbatim: "our
// own extension wrote it" buys nothing, because any local process can post the
// same body. What is new is the CONSEQUENCE. A transcript path is read; a
// socket path is *acted on* — herdrClientLogPath derives the client log beside
// it and the darwin host probe scans for whoever holds that file open, then
// adopts that process's terminal as the session's host window. An unconfined
// path would therefore let any local process choose which window irrlicht's
// click-to-focus raises.
//
// So the path is confined to herdr's own tree before it is stored, by the four
// properties below, and a hint failing any of them is dropped in silence: the
// session keeps no pane, which is exactly what it has today.
package pi

import (
	"os"
	"path/filepath"
	"strings"
)

// herdrSocketName is the file name every herdr control socket has, for the
// default session (<config>/herdr.sock) and for named ones
// (<config>/sessions/<name>/herdr.sock) alike. Stated once here and once in
// processlifecycle's herdrClientLogName comment, both from the same
// live-verified layout (herdr 0.8.0).
const herdrSocketName = "herdr.sock"

// maxHerdrPaneIDLen bounds the pane address. herdr's is "w<n>:p<n>" — five or
// six bytes in practice — so this is a sanity ceiling on untrusted input, not
// a real limit; nothing downstream would be harmed by a longer one, and
// nothing legitimate produces it.
const maxHerdrPaneIDLen = 64

// herdrConfigDirFn locates herdr's configuration root. A variable so tests can
// point it at a fixture tree; production takes the documented layout.
//
// $XDG_CONFIG_HOME is deliberately NOT consulted. Whether herdr honours it is
// not something this issue measured, and guessing has an asymmetric cost:
// honouring a variable herdr ignores would move the accepted root away from
// the real one and reject every genuine hint. A user whose herdr lives
// elsewhere loses the pane and keeps today's behaviour, which is the safe
// direction to be wrong in.
var herdrConfigDirFn = defaultHerdrConfigDir

func defaultHerdrConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr")
}

// herdrPaneHint is a validated pane address together with the server socket it
// belongs to.
//
// Both halves are required, and the socket is the half that does the work: a
// pane address like "w1:p1" exists on every running herdr server, so the
// socket is what says WHICH server was meant. It is the socket the daemon
// confirms the report on before believing it, and afterwards the one
// clientHostFor reaches the attached client through. A pane id on its own
// would name a pane that cannot be confirmed and cannot be focused — the
// macOS HerdrActivator requires both for the same reason and says so.
type herdrPaneHint struct {
	paneID     string
	socketPath string
}

// herdrPaneHintFrom validates a reported pane, returning ok=false when it must
// not be stored. An absent hint (both fields empty) is not an error — it is
// every session that is not running under herdr — and reports ok=false without
// distinction, because the caller does the same thing either way.
func herdrPaneHintFrom(paneID, socketPath string) (herdrPaneHint, bool) {
	if !validHerdrPaneID(paneID) {
		return herdrPaneHint{}, false
	}
	confined := confinedHerdrSocketPath(socketPath)
	if confined == "" {
		return herdrPaneHint{}, false
	}
	return herdrPaneHint{paneID: paneID, socketPath: confined}, true
}

// herdrPaneIDAlphabet is every character a herdr pane address may contain.
// herdr's own is "w<n>:p<n>"; the letters and the two separators leave room for
// a naming scheme without admitting anything structural.
//
// Spelled out rather than expressed as ranges, because this constant IS the
// rule: an allowlist is only an improvement on a denylist if a reader can see
// the whole of it at once.
const herdrPaneIDAlphabet = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	":_-"

// validHerdrPaneID reports whether s is shaped like a herdr pane address.
//
// An allowlist rather than a denylist, because the value ends up in persisted
// session state that the macOS app renders and that a future caller may put in
// a request: what may appear in it should be readable rather than inferred from
// what somebody thought to exclude.
//
// Ranging over runes rather than bytes is what rejects a multi-byte character:
// no rune outside the alphabet above is in it, and invalid UTF-8 decodes to
// RuneError, which is not in it either.
func validHerdrPaneID(s string) bool {
	if s == "" {
		return false
	}
	if len(s) > maxHerdrPaneIDLen {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(herdrPaneIDAlphabet, r) {
			return false
		}
	}
	return true
}

// confinedHerdrSocketPath returns p when it is a herdr control socket inside
// herdr's own configuration root, and "" otherwise.
//
// Four properties, each closing a different way in:
//
//   - Absolute and already clean. Rejecting rather than cleaning is the point:
//     a path needing normalisation is not one this extension produced, since
//     it forwards $HERDR_SOCKET_PATH verbatim, and normalising a hostile path
//     into an acceptable one is the classic way a confinement check becomes a
//     confinement bypass.
//   - Named herdr.sock, so the accepted set is one file per directory rather
//     than any file under the root.
//   - Under the configuration root, tested against root + separator so that a
//     sibling directory sharing the root's prefix ("…/herdr-evil/herdr.sock")
//     does not pass a bare string prefix.
//   - Actually a socket, by Lstat. Lstat and not Stat, deliberately: Stat
//     follows symlinks, so a link named herdr.sock placed inside the root and
//     pointing at a socket outside it would satisfy every other property.
//     Lstat sees the link itself, which is not a socket, and rejects.
//
// What this does NOT defend against, stated rather than implied: a symlinked
// DIRECTORY component inside the root — say sessions/ replaced by a link into
// an attacker's tree. Reaching that already requires write access inside
// ~/.config/herdr, and an attacker with that access owns herdr's real socket
// too, so the hint is no longer the weakest way in.
// One predicate per property rather than one function with four guards: each
// answers a question of a different kind — how the string is written, where it
// points, and what is actually there — and they are worth naming separately
// because only the last one touches the filesystem.
func confinedHerdrSocketPath(p string) string {
	if !herdrSocketNamed(p) {
		return ""
	}
	if !underHerdrConfigRoot(p) {
		return ""
	}
	if !isSocketFile(p) {
		return ""
	}
	return p
}

// herdrSocketNamed reports whether p is written the way a captured
// $HERDR_SOCKET_PATH is: absolute, already clean, and named herdr.sock.
//
// Sequential guards rather than one disjunction, because each names a distinct
// way a path can be wrong and a reader looking for one of them should not have
// to parse the other two first.
func herdrSocketNamed(p string) bool {
	if p == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		return false
	}
	if filepath.Clean(p) != p {
		return false
	}
	return filepath.Base(p) == herdrSocketName
}

// underHerdrConfigRoot reports whether p lies inside herdr's configuration
// root. Tested against root + separator so a sibling directory that merely
// shares the root's prefix does not pass.
func underHerdrConfigRoot(p string) bool {
	root := herdrConfigDirFn()
	if root == "" {
		return false
	}
	return strings.HasPrefix(p, filepath.Clean(root)+string(filepath.Separator))
}

// isSocketFile reports whether p is itself a socket. Lstat, so a symlink is
// judged as the link it is rather than as whatever it points at.
func isSocketFile(p string) bool {
	// herdrSocketNamed already rejects parent traversal because such a path is
	// not clean. Keep this stricter check beside Lstat because CodeQL does not
	// carry that validation through the helper calls above. No measured herdr
	// path contains "..", so rejecting the wider spelling fails closed.
	if strings.Contains(p, "..") {
		return false
	}
	info, err := os.Lstat(p)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}
