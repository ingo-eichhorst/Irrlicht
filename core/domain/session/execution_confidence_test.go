package session

import (
	"slices"
	"strings"
	"testing"
)

const (
	decisiveMessage = "I'll update the parser to read the new field, then add a test that " +
		"feeds it a recorded transcript. The fix goes in parseAssistant: it now copies " +
		"the full text before truncation. Running the suite next."
	hedgeHeavyMessage = "I'm not sure what this ticket wants. The requirement is ambiguous — " +
		"it could mean the list view or the detail view, and it's unclear which one. " +
		"Maybe the list view? I might be wrong, though. There are several options here, " +
		"and I don't know which approach is right. Perhaps I should look again."
)

// scoreOf feeds the messages through a fresh accumulator and returns the score.
func scoreOf(t *testing.T, messages ...string) int {
	t.Helper()
	var acc ExecutionConfidenceAccumulator
	for _, m := range messages {
		acc.Observe(MeasureHedging(m))
	}
	s, ok := acc.Score()
	if !ok {
		t.Fatalf("no score after %d messages", len(messages))
	}
	return s
}

func repeat(msg string, n int) []string { return slices.Repeat([]string{msg}, n) }

func TestExecutionConfidence_DecisiveTranscriptScoresHigh(t *testing.T) {
	if got := scoreOf(t, repeat(decisiveMessage, 6)...); got < 80 {
		t.Errorf("decisive transcript scored %d, want >= 80", got)
	}
}

func TestExecutionConfidence_HedgeHeavyTranscriptScoresLow(t *testing.T) {
	if got := scoreOf(t, repeat(hedgeHeavyMessage, 6)...); got > 30 {
		t.Errorf("hedge-heavy transcript scored %d, want <= 30", got)
	}
}

// Each category on its own, in an otherwise decisive message, must cost
// points — a category whose weight silently became 0 would leave this green
// only for that row.
func TestExecutionConfidence_EachScoredCategoryCostsPoints(t *testing.T) {
	cases := map[HedgeCategory]string{
		HedgeExplicitUncertainty: "I'm not sure the cache key is right.",
		HedgeAmbiguity:           "The spec is ambiguous about the cache key.",
		HedgeSelfDoubt:           "On second thought, the cache key is the session id.",
		HedgeOptionParalysis:     "There are several options for the cache key.",
		HedgeSoft:                "The cache key is probably the session id.",
	}
	base := MeasureHedging(decisiveMessage)
	if base.Weight != 0 {
		t.Fatalf("decisive baseline carries hedge weight %v, want 0", base.Weight)
	}
	for cat, sentence := range cases {
		s := MeasureHedging(decisiveMessage + " " + sentence)
		if s.Weight != hedgeCategoryWeights[cat] {
			t.Errorf("%s: %q weighed %v, want exactly the category weight %v", cat, sentence, s.Weight, hedgeCategoryWeights[cat])
		}
		if s.Weight <= 0 {
			t.Errorf("%s: weight %v — a scored category must cost points", cat, s.Weight)
		}
	}
}

// Asking-instead-of-acting phrases already drive `waiting`; by default they
// must not ALSO lower confidence.
func TestExecutionConfidence_AskingPhrasesScoreZero(t *testing.T) {
	for _, msg := range []string{
		"Should I also update the docs?",
		"Do you want me to open the PR now?",
		"Would you like me to run the full suite? Let me know if so.",
	} {
		s := MeasureHedging(msg)
		if s == nil || s.Weight != 0 {
			t.Errorf("%q: weight %+v, want a zero-weight sample", msg, s)
		}
	}
	if got := scoreOf(t, repeat("Should I also update the docs? Do you want me to open the PR?", 5)...); got != 100 {
		t.Errorf("a session that only asks questions scored %d, want 100", got)
	}
}

func TestExecutionConfidence_HedgesInCodeAreIgnored(t *testing.T) {
	msg := "Here is the output:\n```\n# not sure why, maybe ambiguous, perhaps unclear\n```\n" +
		"and the flag is `--maybe-not-sure`. The fix is in place."
	if s := MeasureHedging(msg); s.Weight != 0 {
		t.Errorf("hedges inside code counted: weight %v, want 0", s.Weight)
	}
	// An unclosed fence runs to the end of the message.
	if s := MeasureHedging("Done.\n```\nnot sure, maybe, unclear"); s.Weight != 0 {
		t.Errorf("hedges inside an unclosed fence counted: weight %v, want 0", s.Weight)
	}
	if s := MeasureHedging("```\nonly code\n```"); s != nil {
		t.Errorf("code-only message produced a sample %+v, want nil", s)
	}
}

func TestExecutionConfidence_TypographicApostropheMatches(t *testing.T) {
	for _, phrase := range []string{"I’m not sure", "I don’t know", "can’t tell"} {
		curly := MeasureHedging("The worker exited cleanly. " + phrase + " why the table was skipped.")
		ascii := MeasureHedging("The worker exited cleanly. " + strings.ReplaceAll(phrase, "’", "'") + " why the table was skipped.")
		if curly.Weight == 0 || curly.Weight != ascii.Weight {
			t.Errorf("%q weighed %v, its ASCII spelling %v — want equal and non-zero", phrase, curly.Weight, ascii.Weight)
		}
	}
}

// A longer phrase containing a shorter one counts once, as the longer one.
func TestExecutionConfidence_OverlappingPhraseCountsOnce(t *testing.T) {
	if s := MeasureHedging("I might be wrong."); s.Weight != hedgeCategoryWeights[HedgeSelfDoubt] {
		t.Errorf("\"I might be wrong\" weighed %v, want the self-doubt weight %v alone", s.Weight, hedgeCategoryWeights[HedgeSelfDoubt])
	}
}

// One message of extreme doubt costs no more than the per-message cap, so it
// cannot pin the score down for dozens of later decisive messages.
func TestExecutionConfidence_OneMessageIsCapped(t *testing.T) {
	s := MeasureHedging(hedgeHeavyMessage)
	limit := executionConfidenceMessageCap * float64(max(s.Words, executionConfidenceCapFloorWords)) / 100
	if s.Weight != limit {
		t.Errorf("hedge-heavy message weighed %v, want it capped at %v", s.Weight, limit)
	}
}

func TestExecutionConfidence_WordBoundaries(t *testing.T) {
	// "mighty", "perhapsly" and "unsureness"-style substrings are not hedges.
	if s := MeasureHedging("The mighty refactor landed; ambiguousness aside, the unclearance test passes."); s.Weight != 0 {
		t.Errorf("substring hits counted: weight %v, want 0", s.Weight)
	}
}

// Early doubt must not haunt a session that later got decisive: five
// hedge-heavy messages followed by fifteen decisive ones recover above the
// low threshold. This is the test the decay exists for — with no decay the
// early hedges would keep the score low.
func TestExecutionConfidence_DecayLetsARecoveredSessionGoHigh(t *testing.T) {
	early := scoreOf(t, repeat(hedgeHeavyMessage, 5)...)
	if early >= ExecutionConfidenceLowThreshold {
		t.Fatalf("precondition: 5 hedge-heavy messages scored %d, want below %d", early, ExecutionConfidenceLowThreshold)
	}
	msgs := append(repeat(hedgeHeavyMessage, 5), repeat(decisiveMessage, 15)...)
	if got := scoreOf(t, msgs...); got < ExecutionConfidenceLowThreshold+20 {
		t.Errorf("5 hedge-heavy then 15 decisive messages scored %d, want >= %d", got, ExecutionConfidenceLowThreshold+20)
	}
}

// The mirror image: a session that was decisive and is floundering NOW must
// read low promptly, within a few messages.
func TestExecutionConfidence_RecentDoubtShowsQuickly(t *testing.T) {
	msgs := append(repeat(decisiveMessage, 20), repeat(hedgeHeavyMessage, 3)...)
	if got := scoreOf(t, msgs...); got >= ExecutionConfidenceLowThreshold {
		t.Errorf("20 decisive then 3 hedge-heavy messages scored %d, want below %d", got, ExecutionConfidenceLowThreshold)
	}
}

func TestExecutionConfidence_SingleShortMessageIsSmoothed(t *testing.T) {
	if got := scoreOf(t, "Maybe."); got < ExecutionConfidenceLowThreshold {
		t.Errorf("a lone one-word \"Maybe.\" scored %d — the prior should keep it out of the low band", got)
	}
}

func TestExecutionConfidence_NoMessagesNoScore(t *testing.T) {
	var acc ExecutionConfidenceAccumulator
	acc.Observe(nil)
	acc.Observe(MeasureHedging(""))
	if _, ok := acc.Score(); ok {
		t.Error("an accumulator that saw no prose reported a score")
	}
	m := &SessionMetrics{ExecutionConfidenceTooltip: "stale", ExecutionConfidenceLow: true}
	ApplyExecutionConfidence(m, acc)
	if m.ExecutionConfidence != nil {
		t.Errorf("no-score apply left a score: %d", *m.ExecutionConfidence)
	}
	if m.ExecutionConfidenceLow {
		t.Error("no-score apply left the low flag set")
	}
	if m.ExecutionConfidenceTooltip != "" {
		t.Errorf("no-score apply left a tooltip: %q", m.ExecutionConfidenceTooltip)
	}
}

func TestApplyExecutionConfidence_SetsScoreLowAndTooltip(t *testing.T) {
	var acc ExecutionConfidenceAccumulator
	for _, msg := range repeat(hedgeHeavyMessage, 4) {
		acc.Observe(MeasureHedging(msg))
	}
	m := &SessionMetrics{}
	ApplyExecutionConfidence(m, acc)
	want, _ := acc.Score()
	if m.ExecutionConfidence == nil || *m.ExecutionConfidence != want {
		t.Fatalf("ExecutionConfidence = %v, want %d", m.ExecutionConfidence, want)
	}
	if !m.ExecutionConfidenceLow {
		t.Errorf("score %d not flagged low", want)
	}
	if !strings.Contains(m.ExecutionConfidenceTooltip, "/100") {
		t.Errorf("tooltip %q does not carry the full score", m.ExecutionConfidenceTooltip)
	}
}

func TestApplyExecutionConfidence_DecisiveIsHighAndNotLow(t *testing.T) {
	var acc ExecutionConfidenceAccumulator
	acc.Observe(MeasureHedging(decisiveMessage))
	m := &SessionMetrics{ExecutionConfidenceLow: true}
	ApplyExecutionConfidence(m, acc)
	if m.ExecutionConfidence == nil || *m.ExecutionConfidence != 100 {
		t.Errorf("decisive apply: score %v, want 100", m.ExecutionConfidence)
	}
	if m.ExecutionConfidenceLow {
		t.Error("decisive apply left the low flag set")
	}
}

func TestHedgeSamplePlus(t *testing.T) {
	a := &HedgeSample{Weight: 1, Words: 10}
	if got := (*HedgeSample)(nil).Plus(a); got != a {
		t.Error("nil.Plus(a) != a")
	}
	if got := a.Plus(nil); got != a {
		t.Error("a.Plus(nil) != a")
	}
	if got := a.Plus(&HedgeSample{Weight: 2, Words: 5}); got.Weight != 3 || got.Words != 15 {
		t.Errorf("Plus = %+v, want {3 15}", got)
	}
}

// BenchmarkMeasureHedging measures the uncached scorer over mixed decisive and
// hedge-heavy prose.
func BenchmarkMeasureHedging(b *testing.B) {
	text := strings.Repeat(decisiveMessage+" "+hedgeHeavyMessage+" ", 40)
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		MeasureHedging(text)
	}
}

// TestExecutionConfidence_DocumentedExamples pins the worked examples the
// constants' doc comments quote, so a tuning change that moves them has to
// update the comments too.
func TestExecutionConfidence_DocumentedExamples(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	score := func(s *HedgeSample) int {
		var acc ExecutionConfidenceAccumulator
		acc.Observe(s)
		v, _ := acc.Score()
		return v
	}
	if got := score(&HedgeSample{Weight: 3, Words: 120}); got != 63 {
		t.Errorf("one \"not sure\" in a 120-word reply scored %d, the saturation comment says 63", got)
	}
	if got := score(&HedgeSample{Weight: 6, Words: 120}); got != 25 {
		t.Errorf("two explicit-uncertainty hits in a 120-word reply scored %d, the saturation comment says 25", got)
	}
	// Cap: a fully capped message of 40 words scores 0; a shorter one does not.
	if got := score(MeasureHedging("not sure unclear ambiguous no idea " + words(34))); got != 0 {
		t.Errorf("a capped 40-word message scored %d, the cap comment says 0", got)
	}
	if got := score(MeasureHedging("not sure unclear ambiguous no idea " + words(24))); got != 14 {
		t.Errorf("a capped 30-word message scored %d, the cap comment says 14", got)
	}
	// Cap floor: a short "I'm not sure." keeps its full weight 3, and the
	// categories stay ordered for short replies.
	if s := MeasureHedging("I'm not sure."); s.Weight != hedgeCategoryWeights[HedgeExplicitUncertainty] {
		t.Errorf("\"I'm not sure.\" weighed %v, the cap-floor comment says %v", s.Weight, hedgeCategoryWeights[HedgeExplicitUncertainty])
	}
	if a, b := MeasureHedging("It is ambiguous."), MeasureHedging("I'm not sure."); a.Weight >= b.Weight {
		t.Errorf("short reply: ambiguity %v not below explicit uncertainty %v", a.Weight, b.Weight)
	}
}

func TestExecutionConfidence_Inflections(t *testing.T) {
	for _, msg := range []string{
		"There is some uncertainty about the API.",
		"These ambiguities remain.",
		"The ticket is worded ambiguously.",
		"It's not clear.",
		"UNCLEAR what the caller expects.",
	} {
		if s := MeasureHedging(msg); s.Weight == 0 {
			t.Errorf("%q weighed 0, want a hit", msg)
		}
	}
}

// The cached entry point returns the same samples as the pure one, nil
// included, and hands out copies (a lock: entries are stored by value).
func TestMeasureHedgingCached_MatchesUncached(t *testing.T) {
	for _, text := range []string{hedgeHeavyMessage, decisiveMessage, "```\nonly code\n```", ""} {
		want := MeasureHedging(text)
		for i := 0; i < 2; i++ { // miss, then hit
			got := MeasureHedgingCached(text)
			if (got == nil) != (want == nil) || (got != nil && *got != *want) {
				t.Fatalf("pass %d on %.20q: cached %+v, uncached %+v", i, text, got, want)
			}
			if got != nil {
				got.Weight = 999
			}
		}
	}
}

// A working set that fits one generation survives a turnover: the memo keeps
// the previous generation readable instead of dropping everything at once (the
// clear-all cliff the #737 simplify review measured at 116x slower).
func TestHedgeSampleMemo_WorkingSetSurvivesTurnover(t *testing.T) {
	c := &hedgeSampleMemo{cur: make(map[uint64]HedgeSample)}
	s := &HedgeSample{Weight: 1, Words: 1}
	for k := uint64(0); k < hedgeMemoGeneration; k++ {
		c.put(k, s)
	}
	c.put(1<<40, s) // forces the turnover
	misses := 0
	for k := uint64(0); k < hedgeMemoGeneration; k++ {
		if _, ok := c.get(k); !ok {
			misses++
		}
	}
	if misses != 0 {
		t.Errorf("%d of %d working-set entries missed after one turnover, want 0", misses, hedgeMemoGeneration)
	}
}
