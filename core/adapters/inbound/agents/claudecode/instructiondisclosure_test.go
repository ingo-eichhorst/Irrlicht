// instructiondisclosure_test.go binds the instructions permission's consent
// copy to what applyInstructionBlocks actually writes into the user's
// ~/.claude/CLAUDE.md (issue #1377) — the same obligation
// contracttesting.AssertHookDisclosureMatchesInstalled carries for the hooks
// permission (#1356), which is keyed on hook events and so structurally cannot
// see this one.
//
// It lives here rather than in contracttesting on purpose: today exactly one
// adapter installs instruction blocks, and the generalized seam the issue
// sketches (an inventory on agent.Permission spanning every KindModify
// permission) is owned by #1383. This file asserts the obligation for the
// adapter that has it; if a second adapter grows managed instruction blocks,
// promote it then.
//
// Ground truth is the file on disk, not the inventory the renderer reads: the
// test runs the real Apply effect against a temp HOME and scans the BEGIN
// sentinels that ended up written. A test comparing the copy against the same
// variable the copy is rendered from would be comparing a declaration to
// itself.
package claudecode

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"irrlicht/core/domain/permission"
)

// blockSentinelName pulls a managed block's name out of its BEGIN sentinel:
// "<!-- BEGIN IRRLICHT MANAGED BLOCK (task-eta) -->" → "task-eta". Applied both
// to the file the installer wrote and to the package's sentinel constants, so
// neither side is a hand-kept roster of names.
var blockSentinelName = regexp.MustCompile(`BEGIN IRRLICHT MANAGED BLOCK \(([a-z0-9-]+)\)`)

// blockCountPattern reads back every block count the consent copy states.
// "irrlicht-" is optional so the Touches line ("2 managed blocks") and the
// Detail line ("2 irrlicht-managed blocks") are both recognized, and the
// singular is accepted so a one-block future is not forced into bad grammar.
var blockCountPattern = regexp.MustCompile(`(\d+) (?:irrlicht-)?managed blocks?`)

// namePattern matches a block name as prose would carry it: the hyphen in
// "task-summary" also matches the space in "task summary". That tolerance is
// load-bearing for the over-promise arm — the copy this test was written
// against disclosed the retired block as "a one-line task summary", which a
// strict hyphenated match would have read as absent.
func namePattern(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + strings.ReplaceAll(regexp.QuoteMeta(name), `-`, `[- ]`) + `\b`)
}

// declaredBlockNames are the names of every managed block this package knows a
// sentinel for — installed, or retired and cleaned up. Referencing the
// constants (not string literals) keeps the list tied to the declarations it
// polices; a future block whose sentinel is not added here is simply not
// covered by the over-promise arm, never silently mis-covered.
func declaredBlockNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, sentinel := range []string{
		taskEtaBeginSentinel,
		taskSummaryBeginSentinel,
		taskQuestionBeginSentinel,
	} {
		m := blockSentinelName.FindStringSubmatch(sentinel)
		if m == nil {
			t.Fatalf("sentinel %q does not carry a parseable block name", sentinel)
		}
		names = append(names, m[1])
	}
	return names
}

// installedBlockNames runs the permission's real Apply effect against a temp
// HOME and returns the block names it actually left in the file.
func installedBlockNames(t *testing.T) []string {
	t.Helper()
	home := withTempHome(t)
	if err := applyInstructionBlocks(); err != nil {
		t.Fatalf("applyInstructionBlocks: %v", err)
	}
	content := readFileString(t, memoryPathFor(home))
	var names []string
	for _, m := range blockSentinelName.FindAllStringSubmatch(content, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal("apply wrote no managed block at all — the test cannot see what it discloses")
	}
	return names
}

// TestInstructionsDisclosure_MatchesInstalledBlocks is the issue #1377 defect
// test. The wizard's Touches/Detail text *is* the #570 consent contract for a
// modify-kind permission, so it must name every block the install writes, state
// how many there are, and present no block it does not write.
func TestInstructionsDisclosure_MatchesInstalledBlocks(t *testing.T) {
	installed := installedBlockNames(t)
	perm := findPermission(t, Agent(), PermissionKeyInstructions)

	if perm.Kind != permission.KindModify {
		t.Errorf("instructions permission is %v, want %v — it writes to the user's CLAUDE.md",
			perm.Kind, permission.KindModify)
	}

	// Touches is the wizard row and Detail the (i) expander; which of the two
	// carries a given fact is presentation, and the user sees both before
	// granting — so the naming and count arms read them together.
	disclosure := perm.Touches + "\n" + perm.Detail

	t.Run("names_every_installed_block", func(t *testing.T) {
		for _, name := range installed {
			if !namePattern(name).MatchString(disclosure) {
				t.Errorf("consent copy never names the %q block, but Apply writes it to "+
					"~/.claude/CLAUDE.md — an undisclosed write to a user-authored file (#570)", name)
			}
		}
	})

	t.Run("states_the_installed_count", func(t *testing.T) {
		matches := blockCountPattern.FindAllStringSubmatch(disclosure, -1)
		if len(matches) == 0 {
			t.Fatalf("consent copy states no block count; want %q",
				strconv.Itoa(len(installed))+" managed blocks")
		}
		for _, m := range matches {
			stated, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("unparseable block count %q: %v", m[1], err)
			}
			if stated != len(installed) {
				t.Errorf("consent copy declares %d managed blocks, Apply writes %d", stated, len(installed))
			}
		}
	})

	t.Run("names_no_uninstalled_block", func(t *testing.T) {
		// Title and FeatureUnlocked join this arm: they are the wizard's row
		// label and benefit line, and naming a retired block in either
		// advertises a capability the install does not deliver. They are
		// deliberately left out of the two arms above — they sell the feature,
		// they do not enumerate the writes. Title matters most here because it
		// is the last hand-written string in this permission, which is exactly
		// the shape of drift the rest of the change makes unrepresentable.
		overPromise := strings.Join([]string{disclosure, perm.FeatureUnlocked, perm.Title}, "\n")
		written := map[string]bool{}
		for _, name := range installed {
			written[name] = true
		}
		for _, name := range declaredBlockNames(t) {
			if written[name] {
				continue
			}
			if namePattern(name).MatchString(overPromise) {
				t.Errorf("consent copy names the %q block, but Apply does not write it — "+
					"the disclosure promises something the install never does; install it "+
					"or drop it from the copy", name)
			}
		}
	})
}
