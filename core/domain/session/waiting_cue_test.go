package session

import "testing"

func TestIsWaitingForUserInput_TrailingMarkdown(t *testing.T) {
	// Models routinely wrap questions in markdown; the literal last
	// byte is often a delimiter, not '?'. Pin that the classifier
	// strips trailing markdown noise before the check.
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain", "What now?", true},
		{"trailing whitespace", "What now?   \n", true},
		{"bold", "**What now?**", true},
		{"italic asterisk", "*What now?*", true},
		{"italic underscore", "_What now?_", true},
		{"strikethrough", "~~What now?~~", true},
		{"inline code", "`What now?`", true},
		{"quoted", "\"What now?\"", true},
		{"single-quoted", "'What now?'", true},
		{"mixed bold + whitespace", "**What now?**\n", true},
		{"production gemma case (asterisks)", "Are there any conventions you follow?**", true},
		{"parenthetical", "Is this true (yes/no)?)", true},
		{"bracketed citation", "Did you mean foo?]", true},
		{"statement", "I am done.", false},
		{"declarative ending in *", "**Done**", false},
		{"empty", "", false},
		{"only delimiters", "***", false},
		// Mid-paragraph questions — issue #236.
		{"mid-paragraph question with trailing status", "What would you like? In the meantime I'll move step 7 to blocked.", true},
		{"question on first line, status after newline", "Do you want me to refactor?\nLet me know.", true},
		{"two questions, both detected", "What's first? Or what's second?", true},
		{"URL with ? not a question", "See https://example.com/?foo=bar for details.", false},
		{"abbreviation e.g. is not a question", "Use a fixture, e.g. small.json. The tests pass.", false},
		// Rhetorical Q&A — agent answers itself, not waiting on user.
		{"joke with Because answer", "Why do programmers prefer dark mode? Because light attracts bugs.", false},
		{"joke with Because answer across newline", "Why do programmers prefer dark mode?\nBecause light attracts bugs.", false},
		{"Since-prefixed answer is rhetorical", "Why bother? Since the cache already has it, we skip.", false},
		{"rhetorical Q followed by real Q", "Why? Because reasons. Should I proceed?", true},
		// Non-Latin question marks — issue #933.
		{"CJK full-width question mark", "続けますか？", true},
		{"Arabic question mark", "هل تريد أن أتابع؟", true},
		{"Greek question mark", "Θέλετε να συνεχίσω;", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &SessionMetrics{LastAssistantText: c.text}
			if got := m.IsWaitingForUserInput(); got != c.want {
				t.Errorf("text=%q: got %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestExtractQuestionSnippet(t *testing.T) {
	// Issue #236: when a question is detected, the rendered snippet should be
	// the question sentence — not the full surrounding paragraph.
	cases := []struct {
		name string
		text string
		want string
	}{
		{"plain", "What now?", "What now?"},
		{"mid-paragraph trims trailing status", "What would you like? In the meantime I'll move step 7 to blocked and not touch your daemon.", "What would you like?"},
		{"multi-question keeps first (top-level question over bullet options)", "What's first? Or what's second?", "What's first?"},
		{"bullet options after lead question keep lead", "what would you like me to test? For example:\n- run tests?\n- something else?", "what would you like me to test?"},
		{"newline-separated", "Looking at the code.\nDo you want me to refactor?\nLet me know.", "Do you want me to refactor?"},
		{"bold-wrapped preserved", "**What now?**", "**What now?**"},
		{"trailing whitespace stripped", "What now?   \n", "What now?"},
		{"leading ellipsis from truncation", "…end of context. Hello, what's next?", "Hello, what's next?"},
		{"no question returns empty", "Done. The tests pass.", ""},
		{"empty input", "", ""},
		{"only punctuation", "***", ""},
		// Rhetorical Q&A is skipped — the agent isn't waiting on the user.
		{"joke with Because answer returns empty", "Why do programmers prefer dark mode? Because light attracts bugs.", ""},
		{"joke with Because across newline returns empty", "Why do programmers prefer dark mode?\nBecause light attracts bugs.", ""},
		{"rhetorical Q followed by real Q returns the real one", "Why? Because reasons. Should I proceed?", "Should I proceed?"},
		{"non-answer continuation is not rhetorical", "What would you like? In the meantime I'll move on.", "What would you like?"},
		// Non-Latin question marks — issue #933.
		{"CJK full-width question mark", "続けますか？", "続けますか？"},
		{"CJK question with trailing status, no space (no inter-sentence spaces)", "続けますか？念のため確認します。", "続けますか？"},
		{"Arabic question mark", "هل تريد أن أتابع؟", "هل تريد أن أتابع؟"},
		{"Greek question mark", "Θέλετε να συνεχίσω;", "Θέλετε να συνεχίσω;"},
		{"CJK question wrapped in bold markdown", "**続けますか？**", "**続けますか？**"},
		// Rhetorical Q&A veto in other languages — issue #933.
		{"de: rhetorical weil-answer returns empty", "Warum? Weil der Cache das schon hat.", ""},
		{"es: rhetorical porque-answer returns empty", "¿Por qué? Porque el caché ya lo tiene.", ""},
		// Cross-language word collisions in the rhetorical veto — issue #933
		// code review. German "da" ("since") and French "car" ("because")
		// are also common English words/names; a bare-prefix veto on them
		// would wrongly suppress a real English question followed by an
		// unrelated sentence that happens to start with the same word.
		{"en: 'Da Vinci' does not trigger German da-veto", "Should we proceed? Da Vinci's sketches are relevant background.", "Should we proceed?"},
		{"en: 'Car trouble' does not trigger French car-veto", "What broke the build? Car analogies aside, let's debug.", "What broke the build?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractQuestionSnippet(c.text); got != c.want {
				t.Errorf("text=%q: got %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestExtractWaitingCue(t *testing.T) {
	// Issue #381: agents often end a turn with an imperative or implicit
	// cue rather than a literal `?`. ExtractWaitingCue covers that gap.
	// Each case mirrors a row in the coverage matrix.
	cases := []struct {
		name string
		text string
		want bool // true = a cue is detected
	}{
		// 24 of the 30 matrix rows from issue #381; the remaining 6 are
		// `?`-bearing and stay the responsibility of ExtractQuestionSnippet
		// (covered by TestExtractQuestionSnippet).
		// Cases 1 and 2 quote the issue verbatim — these are the original
		// reported regressions and are the most load-bearing assertions.
		{"1 verbatim issue example", "PR #379 is marked draft. Take a look at the icon and let me know if it's right or needs tweaking before I commit and push phase 2.", true},
		{"2 verbatim issue example", "Bumped settings frame from 560pt → 640pt to accommodate the new toggle section + About footer. Restart done — try the Settings menu again and confirm the Done button is visible.", true},
		{"5 ready for your review", "Pushed PR #379 as draft. Ready for your review.", true},
		{"6 awaiting + before I", "Awaiting your go-ahead before I merge.", true},
		{"7 ping me", "Ping me when you've tested it.", true},
		{"8 your call", "Two options — A) revert, B) re-roll. Your call.", true},
		{"11 let me know", "Let me know when you're back online.", true},
		{"12 confirm the migration", "Confirm the migration ran.", true},
		{"14 holler", "Holler if anything looks off.", true},
		{"15 sign off", "Sign off on the diff when you can.", true},
		{"16 approve the staging", "Approve the staging deploy in #ops to continue.", true},
		{"17 need your input", "Need your input on whether to keep the fallback.", true},
		{"18 awaiting confirmation", "Awaiting confirmation that the cert installed.", true},
		{"19 drop the API key", "Drop the API key in .env and I'll re-run.", true},
		{"20 once you've reviewed", "Once you've reviewed, ship it.", true},
		{"21 I'll wait", "I'll wait for your green light before pushing.", true},
		{"21b I'll wait until you're free", "I'll wait until you're free to look at this.", true},
		{"21c I'll wait on your input", "I'll wait on your input before proceeding.", true},
		{"23 lmk", "Lmk if you'd rather I split the PR.", true},
		{"24 tell me", "Tell me which approach you prefer.", true},
		{"25 please review", "Please review the diff.", true},
		{"29 stop me if", "Heads up — about to drop the table. Stop me if that's wrong.", true},
		{"30 verify locally + reply with", "Verify locally and reply with the diff output.", true},
		// The `?`-bearing rows in the matrix — verified here too because
		// IsWaitingForUserInput ORs both detectors; the cue detector
		// should be permissive enough that several still match independently.
		{"4 wdyt", "WDYT", true},
		{"22 thoughts trailing", "All good. Thoughts", true},
		{"27 any feedback", "Any feedback before I merge", true},

		// Negative cases — must NOT trigger the cue detector.
		{"neg done tests pass", "Done. The tests pass.", false},
		{"neg all green pushed", "All green. Pushed to main.", false},
		{"neg confirmed past tense", "Confirmed: the migration ran cleanly.", false},
		{"neg URL with ?", "See https://example.com/?foo=bar for details.", false},
		{"neg test failures substring", "Use a fixture, e.g. small.json. The tests pass.", false},
		{"neg empty", "", false},
		{"neg statement", "I am done.", false},
		// #897: "I'll wait" on a background subagent's own completion is not
		// a wait on the user — vetoed by waitingCueExclusions ("its"/"their"/
		// "it" as the wait object), not by narrowing the inclusion pattern
		// (that regressed recall for genuine human-directed waits lacking a
		// literal "for you"/"for your", per code review on this fix).
		{"neg wait for background agent (its)", "The Go agent is doing significant interface-renaming work — I'll wait for its completion notification before touching any Go/JS files myself.", false},
		{"neg wait for background agents (their)", "The Go and JS agents are still running — I'll wait for their completion before merging.", false},
		{"neg wait for it", "The background job is almost done — I'll wait for it to finish before continuing.", false},

		// Non-English cue patterns — issue #933.
		{"de: sag mir bescheid", "Sag mir Bescheid, bevor ich fortfahre.", true},
		{"de: bitte + verb", "Bitte prüfe die Änderungen.", true},
		{"es: avísame", "Avísame si quieres que continúe.", true},
		{"es: por favor + verb", "Por favor confirma el despliegue.", true},
		{"fr: dis-moi", "Dis-moi si tu veux que je continue.", true},
		{"fr: s'il te plaît + verb", "S'il te plaît vérifie le diff.", true},
		{"pt: me avise", "Me avise se quiser que eu continue.", true},
		{"pt: por favor + verb", "Por favor confirme o deploy.", true},
		{"ja: kakunin shite kudasai", "変更を確認してください。", true},
		{"zh: qing quren", "请确认部署是否正确。", true},
		// NFD-decomposed (combining-mark) input — issue #933 code review.
		// The i18n patterns use precomposed (NFC) accented runes; a
		// transcript source emitting NFD text (base letter + combining
		// mark as separate runes) must still match after foldCombiningDiacritics.
		{"es: NFD-decomposed avísame still matches", "Avísame si quieres que continúe.", true},
		{"fr: NFD-decomposed plaît still matches", "S'il te plaît vérifie le diff.", true},
		// Non-English negatives — plain statements must not trigger.
		{"neg de: statement", "Ich habe die Änderungen vorgenommen.", false},
		{"neg fr: statement", "J'ai terminé les modifications.", false},
		// Ambiguous formal-possessive false positives — issue #933 code
		// review. "ihre"/"su"/"sua" mean her/their/formal-your all at once
		// in these languages (unlike English "its"/"their"), so a background
		// job's completion described with the formal possessive must not be
		// misread as addressed to the user, mirroring #897 for English.
		{"neg de: warte auf ihre (ambiguous, background job)", "Der andere Agent arbeitet noch. Ich warte auf ihre Fertigstellung.", false},
		{"neg es: esperando su (ambiguous, background job)", "El otro agente sigue trabajando — estoy esperando su finalización.", false},
		{"neg pt: aguardo sua (ambiguous, background job)", "O outro agente ainda está rodando — aguardo sua finalização.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractWaitingCue(c.text) != ""
			if got != c.want {
				t.Errorf("ExtractWaitingCue(%q) detected=%v, want %v (snippet=%q)", c.text, got, c.want, ExtractWaitingCue(c.text))
			}
		})
	}
}

func TestIsWaitingForUserInput_ImperativeCues(t *testing.T) {
	// Public-API assertion: with the cue detector OR'd into the question
	// detector, turns that end with an imperative gate now register as
	// waiting. Sample the most representative shapes from issue #381 so a
	// future regression at either layer surfaces here too.
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"take a look + let me know", "Take a look at the icon and let me know if it's right.", true},
		{"ready for your review", "Pushed PR #379 as draft. Ready for your review.", true},
		{"your call", "Two options — A) revert, B) re-roll. Your call.", true},
		{"awaiting + before I", "Awaiting your go-ahead before I merge.", true},
		{"once you've reviewed", "Once you've reviewed, ship it.", true},
		{"please review", "Please review the diff.", true},
		{"stop me if", "Heads up — about to drop the table. Stop me if that's wrong.", true},

		// Done-state regression guards — must stay false.
		{"done tests pass stays ready", "Done. The tests pass.", false},
		{"all green pushed stays ready", "All green. Pushed to main.", false},
		{"confirmed past tense stays ready", "Confirmed: the migration ran cleanly.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &SessionMetrics{LastAssistantText: c.text}
			if got := m.IsWaitingForUserInput(); got != c.want {
				t.Errorf("text=%q: got %v, want %v", c.text, got, c.want)
			}
		})
	}
}
