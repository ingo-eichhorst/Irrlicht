// execution_confidence.go is the single source of truth for the
// execution-confidence score (issue #737): how sure an agent sounds about the
// work it is doing, read from hedging and uncertainty language in its own
// assistant prose. Everything tunable lives in this file — the lexicon and its
// category weights, the decay half-life, the smoothing prior, the saturation
// point and the "low" threshold — so tuning is one edit, and the adapters,
// the tailer, the replay converter and the store folds only ever call into it.
//
// The pipeline, end to end:
//
//  1. An adapter parser that holds an assistant message's FULL text (never the
//     200-rune display tail) calls MeasureHedging on it and puts the sample on
//     the parsed event.
//  2. ExecutionConfidenceAccumulator folds the samples, decaying older ones by
//     assistant MESSAGE count — not wall-clock — so replay stays deterministic.
//  3. ApplyExecutionConfidence turns the accumulator into the three
//     SessionMetrics fields both UIs read: the 0–100 score, the low flag, and
//     a daemon-composed tooltip.
//
// Purely lexical and local: no model call, no network — Irrlicht stays a
// passive observer, and the same transcript always yields the same score.
package session

import (
	"fmt"
	"hash/maphash"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// HedgeCategory groups lexicon phrases by the kind of doubt they express. The
// category, not the individual phrase, carries the weight.
type HedgeCategory string

const (
	// HedgeExplicitUncertainty: the agent says outright that it does not know.
	HedgeExplicitUncertainty HedgeCategory = "explicit_uncertainty"
	// HedgeAmbiguity: the agent flags the task or the code as ambiguous.
	HedgeAmbiguity HedgeCategory = "ambiguity"
	// HedgeSelfDoubt: the agent backtracks on something it already said.
	HedgeSelfDoubt HedgeCategory = "self_doubt"
	// HedgeOptionParalysis: the agent enumerates options instead of choosing.
	HedgeOptionParalysis HedgeCategory = "option_paralysis"
	// HedgeSoft: low-commitment qualifiers ("maybe", "probably").
	HedgeSoft HedgeCategory = "soft_hedge"
	// HedgeAsking: asking the user instead of acting ("should I …?").
	HedgeAsking HedgeCategory = "asking"
)

// hedgeCategoryWeights is the weight one phrase hit in each category adds to a
// message's hedge weight. Explicit uncertainty and ambiguity weigh most —
// they are the language of an agent that has lost the thread; a soft "maybe"
// is ordinary technical caution and weighs least.
//
// HedgeAsking is 0 BY DEFAULT, deliberately: those phrases already drive the
// session to `waiting` through ProseIndicatesWaiting, and an agent that
// legitimately stops to ask a question must not ALSO be marked as
// low-confidence for it. Its phrases stay in the lexicon so a tuning change
// can switch the category on with one number.
var hedgeCategoryWeights = map[HedgeCategory]float64{
	HedgeExplicitUncertainty: 3,
	HedgeAmbiguity:           2.5,
	HedgeSelfDoubt:           2,
	HedgeOptionParalysis:     1.5,
	HedgeSoft:                1,
	HedgeAsking:              0,
}

// hedgeLexicon is the phrase table. Each entry is a LOWER-CASE RE2 fragment
// matched on word boundaries against code-stripped, lower-cased prose whose
// typographic apostrophes were normalised to ASCII. Overlapping phrases are
// resolved longest-first (see hedgeMatcher), so "I might be wrong" counts once
// as self-doubt rather than also as the soft hedge "might"
// (TestExecutionConfidence_OverlappingPhraseCountsOnce). Because every phrase
// is matched as a whole word, inflections need their own spelling here
// ("uncertainty", "ambiguities").
var hedgeLexicon = map[HedgeCategory][]string{
	HedgeExplicitUncertainty: {
		`i don'?t know`, `i do not know`, `not sure`, `unsure`, `uncertain`,
		`uncertaint(?:y|ies)`, `not certain`, `unclear`, `not clear`, `hard to say`, `no idea`, `can'?t tell`,
		`cannot tell`, `i'?m confused`, `i am confused`, `not confident`,
	},
	HedgeAmbiguity: {
		`ambiguous(?:ly)?`, `ambiguit(?:y|ies)`, `could mean`, `not obvious`,
		`assuming that`, `i'?m assuming`, `i am assuming`,
		`open to interpretation`, `hard to interpret`,
	},
	HedgeSelfDoubt: {
		`i might be wrong`, `i may be wrong`, `i could be wrong`,
		`actually,? wait`, `on second thought`, `let me reconsider`,
		`let me rethink`, `i was wrong`, `my mistake`, `scratch that`,
	},
	HedgeOptionParalysis: {
		`(?:multiple|several|a few|two|various) (?:options|approaches|ways to)`,
		`we could either`, `could either`, `one approach`, `another approach`,
		`alternatively`,
	},
	HedgeSoft: {
		`maybe`, `perhaps`, `might`, `probably`, `possibly`, `i think`,
		`i believe`, `i guess`, `it seems`, `seems like`, `seems to`,
	},
	HedgeAsking: {
		`should i`, `shall i`, `do you want me to`, `would you like me to`,
		`let me know if`, `want me to`,
	},
}

// executionConfidenceHalfLifeMessages is how many assistant messages it takes
// for a hedge sample's influence to halve. Five: the maintainer's suggested
// order of magnitude (issue #737, decision 2), and short enough that a burst of
// doubt at the start of a long, later-decisive run fades within a handful of
// replies, long enough that one stray "maybe" in the latest reply does not
// swing the score on its own. The decay counts assistant messages (one per
// text-bearing assistant event), not wall-clock, so replay is deterministic.
const executionConfidenceHalfLifeMessages = 5.0

// executionConfidencePriorWords is a smoothing prior: the density divides by
// (decayed words + this many words) rather than the decayed words alone, so a
// session whose only message is "Maybe." does not score 0 off a single word.
// It acts like 40 words of neutral prose already seen.
const executionConfidencePriorWords = 40.0

// executionConfidenceSaturation is the density at which the score bottoms out
// at 0, where density = 100 × decayed weight / (decayed words + the prior):
// score = round(100 × (1 − density / saturation)), clamped to 0–100. The low
// threshold is therefore crossed at a density of 2.5. Worked examples for a
// session of ONE 120-word reply, each pinned by
// TestExecutionConfidence_DocumentedExamples: one "not sure" (weight 3)
// scores 63, not low; two explicit-uncertainty hits (weight 6) score 25, low.
const executionConfidenceSaturation = 5.0

// executionConfidenceMessageCap bounds one message's hedge weight to this many
// weighted hits per 100 words (over at least executionConfidenceCapFloorWords
// words). Without it a single paragraph of pure doubt — 35 weighted hits per
// 100 words is easy to write — keeps the score low for dozens of later,
// decisive messages, because one eighth of an extreme still outweighs a lot of
// neutral prose; the cap makes "one awful message" and "one bad message" cost
// the same, so recovery is a matter of messages, not of how loud the doubt was.
// Twice the saturation, so one capped message of 40 or more words scores 0 on
// its own (at exactly 40 words the prior halves the density to the saturation
// point). Below 40 words the floor and the prior decide: a capped 30-word
// message scores 14, not 0 (TestExecutionConfidence_DocumentedExamples).
const executionConfidenceMessageCap = 2 * executionConfidenceSaturation

// executionConfidenceCapFloorWords is the word count the cap assumes for a
// shorter message. At 30 the floor cap is 3 — the heaviest single category
// weight — so a short "I'm not sure." keeps its full weight 3 instead of being
// capped to a fraction of it, and the categories stay ordered for short
// replies (TestExecutionConfidence_DocumentedExamples).
const executionConfidenceCapFloorWords = 30

// ExecutionConfidenceLowThreshold is the score below which a session is
// flagged low-confidence (ExecutionConfidenceLow) and the UIs show a chip.
const ExecutionConfidenceLowThreshold = 50

// HedgeSample is one assistant message's measurement: the summed category
// weight of every lexicon hit, and the number of prose words it was found in.
type HedgeSample struct {
	Weight float64
	Words  int
}

// Plus returns the sum of two samples, treating nil as "no sample". It lets an
// adapter whose message spans several text blocks measure each block at its
// own call site and still emit ONE sample per message.
func (s *HedgeSample) Plus(o *HedgeSample) *HedgeSample {
	switch {
	case s == nil:
		return o
	case o == nil:
		return s
	}
	return &HedgeSample{Weight: s.Weight + o.Weight, Words: s.Words + o.Words}
}

// hedgeMatcher is the compiled lexicon: one non-capturing alternation over
// lower-cased text, alternatives sorted longest-first so RE2's leftmost-first
// alternation picks the most specific phrase at any position. hedgePhrases
// holds the same alternatives, in the same order, each anchored, to recover a
// match's weight: the first one that matches the matched text exactly is the
// alternative the combined pattern chose. Matching lower-cased input without
// (?i) and without per-phrase capture groups took
// `go test ./core/domain/session -run '^$' -bench MeasureHedging` from 1.4 to
// 5.1 MB/s on the development machine (#737 review).
var hedgeMatcher, hedgePhrases = compileHedgeLexicon()

type hedgePhrase struct {
	exact  *regexp.Regexp
	weight float64
}

func compileHedgeLexicon() (*regexp.Regexp, []hedgePhrase) {
	type entry struct {
		pattern string
		weight  float64
	}
	var entries []entry
	for cat, phrases := range hedgeLexicon {
		for _, p := range phrases {
			entries = append(entries, entry{p, hedgeCategoryWeights[cat]})
		}
	}
	// Longest pattern first, then lexical, so the order (and therefore the
	// match result) does not depend on map iteration.
	sort.Slice(entries, func(i, j int) bool {
		if len(entries[i].pattern) != len(entries[j].pattern) {
			return len(entries[i].pattern) > len(entries[j].pattern)
		}
		return entries[i].pattern < entries[j].pattern
	})
	alts := make([]string, len(entries))
	phrases := make([]hedgePhrase, len(entries))
	for i, e := range entries {
		alts[i] = e.pattern
		phrases[i] = hedgePhrase{regexp.MustCompile(`^(?:` + e.pattern + `)$`), e.weight}
	}
	return regexp.MustCompile(`\b(?:` + strings.Join(alts, "|") + `)\b`), phrases
}

// hedgeWeightOf returns the weight of the lexicon alternative that produced
// match.
func hedgeWeightOf(match string) float64 {
	for _, p := range hedgePhrases {
		if p.exact.MatchString(match) {
			return p.weight
		}
	}
	return 0
}

// fencedCodeBlock matches a ``` or ~~~ fenced block, including one left
// unclosed at the end of the message (it runs to end of text). Hedges inside
// code — quoted command output, comments in a diff — are not the agent's voice.
var fencedCodeBlock = regexp.MustCompile("(?s)(```|~~~).*?(?:```|~~~|$)")

// inlineCode matches a single-line `inline code` span.
var inlineCode = regexp.MustCompile("`[^`\n]*`")

// MeasureHedging scores one assistant message's FULL text. It returns nil when
// the text holds no prose words (empty, or code only), so a code-only reply
// neither raises nor lowers the score. Pure and uncached: the tailer reads each
// line once, so a cache would only cost it a hash per message.
func MeasureHedging(text string) *HedgeSample {
	prose := hedgeProse(text)
	words := countProseWords(prose)
	if words == 0 {
		return nil
	}
	weight := 0.0
	for _, m := range hedgeMatcher.FindAllString(prose, -1) {
		weight += hedgeWeightOf(m)
	}
	limit := executionConfidenceMessageCap * float64(max(words, executionConfidenceCapFloorWords)) / 100
	return &HedgeSample{Weight: math.Min(weight, limit), Words: words}
}

// MeasureHedgingCached is MeasureHedging memoised by a hash of the text, for
// the opencode and hermes store folds, which re-parse a whole session on every
// metrics poll and would otherwise send every assistant byte back through the
// matcher each time. Only those parsers call it, so the memo holds store
// messages only.
func MeasureHedgingCached(text string) *HedgeSample {
	if text == "" {
		return nil
	}
	key := maphash.String(hedgeMemoSeed, text)
	if s, ok := hedgeMemo.get(key); ok {
		return s
	}
	s := MeasureHedging(text)
	hedgeMemo.put(key, s)
	return s
}

// countProseWords counts whitespace-separated runs holding at least one letter
// or digit, without allocating the field slice strings.Fields would.
func countProseWords(s string) int {
	words, counted := 0, false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			counted = false
		case !counted && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			words++
			counted = true
		}
	}
	return words
}

// apostropheFold maps typographic apostrophes to ASCII.
var apostropheFold = strings.NewReplacer("’", "'", "‘", "'")

// hedgeProse strips code, normalises apostrophes and lower-cases, so
// "I’m Not Sure" (a typographic apostrophe, which models emit routinely)
// matches the ASCII lower-case lexicon the same as "i'm not sure". Each
// code-stripping pass runs only when its marker character is present.
func hedgeProse(text string) string {
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		text = fencedCodeBlock.ReplaceAllString(text, " ")
	}
	if strings.IndexByte(text, '`') >= 0 {
		text = inlineCode.ReplaceAllString(text, " ")
	}
	return strings.ToLower(apostropheFold.Replace(text))
}

// hedgeMemoGeneration bounds each memo generation. When the current one fills
// it becomes the previous one and a fresh one starts, so a working set up to
// this size survives the turnover instead of being dropped all at once.
const hedgeMemoGeneration = 8192

var hedgeMemoSeed = maphash.MakeSeed()

// hedgeMemo maps a text hash to its sample, stored by value so callers never
// alias an entry; Words == 0 encodes "no prose" (a real sample always has a
// word). Safe for the concurrent metrics passes of different sessions.
var hedgeMemo = &hedgeSampleMemo{cur: make(map[uint64]HedgeSample)}

type hedgeSampleMemo struct {
	mu        sync.Mutex
	cur, prev map[uint64]HedgeSample
}

func (c *hedgeSampleMemo) get(k uint64) (*HedgeSample, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.cur[k]
	if !ok {
		if s, ok = c.prev[k]; ok {
			c.cur[k] = s // promote, so it survives the next turnover
		}
	}
	if !ok || s.Words == 0 {
		return nil, ok
	}
	return &s, true
}

func (c *hedgeSampleMemo) put(k uint64, s *HedgeSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cur) >= hedgeMemoGeneration {
		c.prev, c.cur = c.cur, make(map[uint64]HedgeSample)
	}
	var v HedgeSample
	if s != nil {
		v = *s
	}
	c.cur[k] = v
}

// ExecutionConfidenceAccumulator is the running, recency-weighted state behind
// the score. Each observed message first decays what came before by
// 2^(-1/half-life), then adds its own weight and words. The tailer owns one per
// session and persists it in its ledger; the store folds (opencode, hermes)
// rebuild one per poll.
type ExecutionConfidenceAccumulator struct {
	DecayedWeight float64 `json:"decayed_weight"`
	DecayedWords  float64 `json:"decayed_words"`
	Messages      int     `json:"messages"`
}

// executionConfidenceDecay is the per-message retention factor.
var executionConfidenceDecay = math.Pow(0.5, 1/executionConfidenceHalfLifeMessages)

// Observe folds one message's sample in. A nil sample (an event with no
// assistant prose) is not a message and leaves the state — including its
// decay — untouched.
func (a *ExecutionConfidenceAccumulator) Observe(s *HedgeSample) {
	if s == nil {
		return
	}
	a.DecayedWeight = a.DecayedWeight*executionConfidenceDecay + s.Weight
	a.DecayedWords = a.DecayedWords*executionConfidenceDecay + float64(s.Words)
	a.Messages++
}

// Score maps the state to 0–100 (100 = decisive). ok is false until at least
// one message has been observed: no prose yet means no score, never a default.
func (a ExecutionConfidenceAccumulator) Score() (score int, ok bool) {
	if a.Messages == 0 {
		return 0, false
	}
	density := 100 * a.DecayedWeight / (a.DecayedWords + executionConfidencePriorWords)
	frac := math.Min(density/executionConfidenceSaturation, 1)
	return int(math.Round(100 * (1 - frac))), true
}

// ApplyExecutionConfidence writes the score, the low flag and the tooltip onto
// m from acc. It is the one place the three fields are derived, called by the
// shared tailer→domain converter (live and replay) and by the store-fold
// metrics paths, so every surface reads the same verdict. With no observed
// message it clears all three.
func ApplyExecutionConfidence(m *SessionMetrics, acc ExecutionConfidenceAccumulator) {
	score, ok := acc.Score()
	if !ok {
		m.ExecutionConfidence = nil
		m.ExecutionConfidenceLow = false
		m.ExecutionConfidenceTooltip = ""
		return
	}
	m.ExecutionConfidence = &score
	m.ExecutionConfidenceLow = score < ExecutionConfidenceLowThreshold
	m.ExecutionConfidenceTooltip = executionConfidenceTooltip(score)
}

func executionConfidenceTooltip(score int) string {
	return fmt.Sprintf("Execution confidence %d/100 — scored from hedging and uncertainty "+
		"language in the agent's recent messages, latest weighted most (100 = decisive; "+
		"below %d is low).", score, ExecutionConfidenceLowThreshold)
}
