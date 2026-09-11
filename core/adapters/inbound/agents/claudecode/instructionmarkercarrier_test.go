// instructionmarkercarrier_test.go holds the issue #1944 tripwire: the check
// that no irrlicht-managed instruction block names the text a user reads as a
// place to put a marker, plus the committed fixtures that keep the tripwire
// honest. It sits apart from instructioninstaller_test.go because it tests a
// property of the block INVENTORY rather than the file-patching machinery that
// file covers.
//
// It is package-local rather than a contracttesting obligation for the reason
// instructiondisclosure_test.go's own header already gives: claudecode is the
// only adapter that installs managed instruction blocks (`git grep -l
// 'ManagedBlockSentinelPrefix\|instructioninstaller'` returns one non-test
// file), so there is no second implementation for a shared contract to hold to
// account yet. Promote it when a second adapter grows blocks, alongside that
// disclosure check.
//
// The rule is an ALLOWLIST, not a negation parser. An earlier version of this
// file tried to tell a prohibition ("never put the marker in your response
// text") apart from an instruction ("or in your response text…") by looking for
// a negating word before the phrase. Review measured three ways through it, all
// reproduced as fixtures below: a fallback appended to the block's own
// "(never to the command itself)" sentence inherited that sentence's negator
// and passed; a conditional phrasing carrying no negator at all passed; and
// natural rewordings of the prohibition itself were REJECTED, because the "!"
// in "<!--" was being treated as a sentence terminator and stranded the
// negator. Parsing English negation is the wrong tool. Every mention of
// rendered text is a finding unless the sentence is registered verbatim below,
// so the failure direction is always safe and always actionable.
package claudecode

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// renderedTextCarrierPhrases are the ways a managed block can refer to the text
// the user reads. A phrase here is not itself a defect — the prohibition has to
// name rendered text to forbid it — it is what makes the guard look at a
// sentence at all. The list is meant as a tripwire for the shapes that have
// actually shipped (issue #1944 removed two), not as a general reading of
// English: it is defeated by an intervening adjective ("your final response"),
// so a reviewer still has to read new block prose.
var renderedTextCarrierPhrases = []string{
	"response text",
	"your response",
	"your reply",
	"your answer",
	"your message",
	"your output",
	"the chat",
	"chat output",
	"visible text",
	"rendered text",
	"assistant text",
}

// permittedCarrierSentences are the exact sentences an installed block is
// allowed to contain that mention rendered text: the prohibitions themselves.
// Every other mention is a finding.
//
// Registering a sentence is deliberately a code change a reviewer sees. The
// cost is that rewording the rule turns the guard red until the new wording is
// registered; that is the intended direction, because the alternative — trying
// to infer intent from the prose — is what findings 2 through 5 of this change's
// review defeated three different ways.
//
// A registered sentence that matches nothing is also a finding
// (TestRenderedTextCarrierGuard_StaleAllowlistEntryIsAFinding), so an entry
// cannot outlive the prose it was written for. Same rule as
// tools/state-vocabulary-lint.waivers: a waiver that stops matching fails too.
var permittedCarrierSentences = []string{
	"That field is the only carrier — never put the marker in your response text, which the user reads",
}

var (
	// instructionFencedBlock matches a ``` … ``` region. The marker examples
	// live in fences and are illustrations, not instructions, so they are
	// removed before the prose is read.
	instructionFencedBlock = regexp.MustCompile("(?s)```.*?```")
	instructionWhitespace  = regexp.MustCompile(`\s+`)
	// instructionSentenceEnd deliberately omits "!": the only "!" in a managed
	// block is the one in "<!--", and treating it as a terminator cut both the
	// sentinels and any prohibition that quotes the marker syntax in half.
	instructionSentenceEnd = regexp.MustCompile(`[.;?]`)
)

// carrierFinding is one sentence the guard objects to.
type carrierFinding struct {
	block    string
	phrase   string
	sentence string
}

func (f carrierFinding) String() string {
	return fmt.Sprintf(
		"block %q mentions rendered text (%q) in a sentence that is not a registered "+
			"prohibition:\n    %q\nIf that sentence STATES the rule, add it verbatim to "+
			"permittedCarrierSentences. If it names rendered text as a place to put a "+
			"marker, remove it — issue #1944: no irrlicht marker may be instructed into "+
			"text the user reads.",
		f.block, f.phrase, f.sentence)
}

// scanBlocksForRenderedTextCarrier returns a finding for every sentence in a
// managed block that mentions rendered text without being a registered
// prohibition, and a second result naming every registered prohibition that
// matched nothing.
//
// An inventory it cannot inspect is itself a finding: an empty slice, or a block
// in the installed list carrying no content, returns a finding rather than an
// empty result, so "found nothing" and "looked at nothing" cannot produce the
// same green (AGENTS.md: a verification mechanism must fail loudly when it
// cannot run). TestRenderedTextCarrierGuard_CannotLookIsNotAPass drives both.
func scanBlocksForRenderedTextCarrier(blocks []managedInstructionBlock) (findings []carrierFinding, unmatchedPermits []string) {
	if len(blocks) == 0 {
		return []carrierFinding{{
			block:    "(none)",
			phrase:   "(none)",
			sentence: "guard inspected zero managed blocks — an empty inventory is not a pass",
		}}, nil
	}
	matched := map[string]bool{}
	for _, b := range blocks {
		blockFindings, blockMatched := scanBlockForRenderedTextCarrier(b)
		findings = append(findings, blockFindings...)
		for _, m := range blockMatched {
			matched[m] = true
		}
	}
	return findings, stalePermittedSentences(matched)
}

// stalePermittedSentences returns the registered prohibitions that matched no
// installed block — allowlist entries that outlived the prose they were written
// for, and now turn the check off over nothing.
func stalePermittedSentences(matched map[string]bool) []string {
	var stale []string
	for _, p := range permittedCarrierSentences {
		if !matched[p] {
			stale = append(stale, p)
		}
	}
	return stale
}

// scanBlockForRenderedTextCarrier reads one block, returning its findings and
// the registered prohibitions it matched (which the caller needs to tell a live
// allowlist entry from a stale one).
func scanBlockForRenderedTextCarrier(b managedInstructionBlock) (findings []carrierFinding, matched []string) {
	if strings.TrimSpace(b.content) == "" {
		return []carrierFinding{{
			block:    b.name,
			phrase:   "(none)",
			sentence: "block is listed as installed but carries no content — nothing was inspected",
		}}, nil
	}
	for _, sentence := range blockProseSentences(b.content) {
		phrase, mentions := renderedTextMention(sentence)
		switch {
		case !mentions:
			// Most sentences never name rendered text at all.
		case permitted(sentence):
			matched = append(matched, sentence)
		default:
			findings = append(findings, carrierFinding{block: b.name, phrase: phrase, sentence: sentence})
		}
	}
	return findings, matched
}

// blockProseSentences reduces a managed block to the sentences a model reads as
// instructions: fenced examples and the BEGIN/END sentinels are machinery, not
// prose, and are dropped before the text is normalized and split.
func blockProseSentences(content string) []string {
	var sentences []string
	for _, s := range instructionSentenceEnd.Split(blockProse(content), -1) {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			sentences = append(sentences, trimmed)
		}
	}
	return sentences
}

// blockProse strips the machinery — fenced marker examples and the BEGIN/END
// sentinel lines — and collapses the block's wrapped lines into one normalized
// run of text, so a phrase broken across a line ("in your\nresponse text")
// reads as one phrase.
func blockProse(content string) string {
	unfenced := instructionFencedBlock.ReplaceAllString(content, " ")
	var kept []string
	for _, line := range strings.Split(unfenced, "\n") {
		if strings.Contains(line, ManagedBlockSentinelPrefix) || strings.Contains(line, managedBlockEndPrefix) {
			continue
		}
		kept = append(kept, line)
	}
	return instructionWhitespace.ReplaceAllString(strings.Join(kept, "\n"), " ")
}

// renderedTextMention reports the first carrier phrase the sentence contains.
func renderedTextMention(sentence string) (string, bool) {
	lower := strings.ToLower(sentence)
	for _, phrase := range renderedTextCarrierPhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}

// permitted reports whether sentence is a registered prohibition. Comparison is
// on the normalized form so a re-wrap of the block's source lines — which
// changes the bytes but not the sentence — does not turn the guard red.
func permitted(sentence string) bool {
	for _, p := range permittedCarrierSentences {
		if sentence == strings.TrimSpace(instructionWhitespace.ReplaceAllString(p, " ")) {
			return true
		}
	}
	return false
}

// TestInstalledInstructionBlocks_NeverInstructMarkerIntoRenderedText is the
// issue #1944 defect test. Seen red against the pre-fix installer (1e5e38dd's
// instructioninstaller.go restored over this tree, `go test
// ./core/adapters/inbound/agents/claudecode/ -run
// NeverInstructMarkerIntoRenderedText -count=1`) with three errors: an
// unregistered carrier in task-eta ("response text"), one in task-question
// ("your response"), and the stale-allowlist error, since the prohibition the
// allowlist registers did not exist yet.
func TestInstalledInstructionBlocks_NeverInstructMarkerIntoRenderedText(t *testing.T) {
	findings, unmatched := scanBlocksForRenderedTextCarrier(installedInstructionBlocks)
	for _, f := range findings {
		t.Errorf("issue #1944: %s", f)
	}
	for _, p := range unmatched {
		t.Errorf("permittedCarrierSentences registers a sentence no installed block contains — "+
			"the prose it was written for is gone, so the entry now permits nothing and hides "+
			"that the prohibition was dropped:\n    %q", p)
	}
}

// reintroducedCarrierFixtures are the ways the deleted carrier could come back.
// Each MUTATES THE SHIPPED BLOCK rather than a stand-in, so the guard is
// exercised against the real surroundings — the fence, the backticked
// identifiers, the em-dash, and the block's own pre-existing "never".
//
// The three marked wasMiss were MEASURED to slip past the negation-parsing
// design this guard replaced (review findings 2 and 3): one inherits the negator
// from the sentence it is spliced into, and two carry a negator the old rule
// read as a prohibition. The unmarked ones that design did catch; they are kept
// so the allowlist is exercised on more than the regressions. All are fixtures
// rather than PR-body prose so the regression is re-run, not remembered.
var reintroducedCarrierFixtures = []struct {
	name    string
	mutate  func(string) string
	wasMiss bool // measured to slip past the pre-review negation-parsing guard
}{
	{
		name:    "fallback spliced into the block's own \"(never to the command itself)\" sentence",
		wasMiss: true,
		mutate: func(block string) string {
			return strings.Replace(block,
				"(never to the command itself).",
				"(never to the command itself), or in your response text when no Bash call is coming.",
				1)
		},
	},
	{
		name:    "conditional phrasing whose only negator is \"are not\"",
		wasMiss: true,
		mutate: func(block string) string {
			return spliceIntoProse(block, "If you are not making a Bash call, put the marker in your response text.")
		},
	},
	{
		name:    "negator opening a sentence that then instructs the carrier",
		wasMiss: true,
		mutate: func(block string) string {
			return spliceIntoProse(block,
				"Don't skip the marker when no Bash call is coming — put it in your response text.")
		},
	},
	{
		name: "imperative with the condition first",
		mutate: func(block string) string {
			return spliceIntoProse(block, "When no Bash call is coming, put the marker in your response text.")
		},
	},
	{
		name: "the exact sentence #1944 deleted, restored in place of the prohibition",
		mutate: func(block string) string {
			return strings.Replace(block,
				"That\nfield is the only carrier — never put the marker in your response text,\nwhich the user reads.",
				"or in your\nresponse text when no Bash call is coming.",
				1)
		},
	},
	{
		name: "the retired task-question end-of-turn carrier (#759), re-added",
		mutate: func(block string) string {
			return spliceIntoProse(block,
				"Emit it on its own line at the very end of your response, only when you are\nactually waiting on the user.")
		},
	},
}

// spliceIntoProse inserts extra into the block body just before the END
// sentinel, so it lands inside the prose the guard reads. Appending AFTER the
// sentinel — what the first version of these fixtures did — put the text in its
// own segment where none of the block's surroundings could influence the
// verdict, making the fixture a duplicate of a minimal stand-in (review
// finding 5).
func spliceIntoProse(block, extra string) string {
	at := strings.LastIndex(block, taskEtaEndSentinel)
	if at < 0 {
		return block + "\n" + extra + "\n"
	}
	return block[:at] + extra + "\n" + block[at:]
}

func TestRenderedTextCarrierGuard_CatchesReintroducedProseCarrier(t *testing.T) {
	for _, fx := range reintroducedCarrierFixtures {
		t.Run(fx.name, func(t *testing.T) {
			mutated := []managedInstructionBlock{{
				name:    "task-eta",
				begin:   taskEtaBeginSentinel,
				end:     taskEtaEndSentinel,
				content: fx.mutate(managedTaskEtaBlock),
			}}
			if mutated[0].content == managedTaskEtaBlock {
				t.Fatal("fixture did not change the block — the mutation's anchor text has moved, " +
					"so this case is silently testing the unmutated block")
			}
			findings, _ := scanBlocksForRenderedTextCarrier(mutated)
			if len(findings) == 0 {
				t.Errorf("guard stayed green on a re-added carrier (wasMiss=%v):\n%s",
					fx.wasMiss, fx.mutate(managedTaskEtaBlock))
			}
		})
	}
}

// TestRenderedTextCarrierGuard_CannotLookIsNotAPass pins the loud-failure half:
// a guard handed nothing to inspect reports that instead of returning clean.
func TestRenderedTextCarrierGuard_CannotLookIsNotAPass(t *testing.T) {
	if findings, _ := scanBlocksForRenderedTextCarrier(nil); len(findings) == 0 {
		t.Error("an empty inventory must produce a finding, not read as a clean pass")
	}
	hollow := []managedInstructionBlock{{
		name: "task-eta", begin: taskEtaBeginSentinel, end: taskEtaEndSentinel,
	}}
	if findings, _ := scanBlocksForRenderedTextCarrier(hollow); len(findings) == 0 {
		t.Error("an installed block with no content must produce a finding, not read as a clean pass")
	}
}

// TestRenderedTextCarrierGuard_StaleAllowlistEntryIsAFinding drives the other
// loud-failure direction: an allowlist is a way to turn a check off, so an entry
// that no longer matches anything must fail rather than sit there permitting a
// sentence nobody writes any more.
func TestRenderedTextCarrierGuard_StaleAllowlistEntryIsAFinding(t *testing.T) {
	stripped := []managedInstructionBlock{{
		name:    "task-eta",
		begin:   taskEtaBeginSentinel,
		end:     taskEtaEndSentinel,
		content: strings.Replace(managedTaskEtaBlock, "That\nfield is the only carrier — never put the marker in your response text,\nwhich the user reads.\n", "", 1),
	}}
	if stripped[0].content == managedTaskEtaBlock {
		t.Fatal("fixture did not change the block — the prohibition's wording has moved")
	}
	findings, unmatched := scanBlocksForRenderedTextCarrier(stripped)
	if len(findings) != 0 {
		t.Errorf("removing the prohibition should leave no carrier finding, got %v", findings)
	}
	if len(unmatched) == 0 {
		t.Error("a registered prohibition that matches nothing must be reported, not silently kept")
	}
}

// TestRenderedTextCarrierGuard_UnregisteredRewordIsRejected documents the
// allowlist's deliberate cost, so the next author meets it here with an
// explanation rather than in CI with a confusing red. A reworded prohibition is
// rejected until it is registered — safe direction, and the finding's own text
// says which list to add it to.
func TestRenderedTextCarrierGuard_UnregisteredRewordIsRejected(t *testing.T) {
	reworded := []managedInstructionBlock{{
		name:  "task-eta",
		begin: taskEtaBeginSentinel,
		end:   taskEtaEndSentinel,
		content: strings.Replace(managedTaskEtaBlock,
			"That\nfield is the only carrier — never put the marker in your response text,\nwhich the user reads.",
			"Your response text must never carry the marker.", 1),
	}}
	findings, _ := scanBlocksForRenderedTextCarrier(reworded)
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding for an unregistered reword, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].String(), "permittedCarrierSentences") {
		t.Errorf("finding must tell the author how to register the sentence:\n%s", findings[0])
	}
}
