package session

import "regexp"

// Non-English waiting-cue patterns, mirroring the same A–E coverage buckets
// as waitingCuePatterns (issue #381) for the languages LLM responses are
// most commonly observed in. See issue #933: the cue detector was English-
// only, so a turn ending on e.g. "Sag mir Bescheid, bevor ich fortfahre"
// never registered as waiting.
//
// No language detection: all buckets are OR'd together regardless of the
// text's language, same as the English set. The false-positive surface of a
// foreign-language phrase appearing inside otherwise-English text (or vice
// versa) is negligible, and recall matters more than precision here (#381).
//
// Coverage is intentionally partial — these are the highest-value phrases
// per language, not an exhaustive translation of every English pattern.
// Extend per-language buckets as new false negatives are reported.
var (
	waitingCuePatternsDE = []*regexp.Regexp{
		// A. Direct ask
		regexp.MustCompile(`(?i)\b(?:sag|sagt|sagen sie)\s+(?:mir|uns)\s+bescheid\b`),
		regexp.MustCompile(`(?i)\blass(?:en sie)?\s+(?:mich|uns)\s+wissen\b`),
		regexp.MustCompile(`(?i)\bkannst du|könnten sie\b`),
		// B. Approval / review framings
		regexp.MustCompile(`(?i)\bwarte(?:n sie)? auf (?:dein|deine|ihr|ihre)\b`),
		regexp.MustCompile(`(?i)\bbereit für (?:dein|deine|ihr|ihre) (?:feedback|freigabe|review)\b`),
		// C. Action gates
		regexp.MustCompile(`(?i)\bbevor ich\b`),
		regexp.MustCompile(`(?i)\bsobald du\b|\bsobald sie\b`),
		regexp.MustCompile(`(?i)\bich warte\b`),
		// D. Curated imperatives
		regexp.MustCompile(`(?i)\bbitte\s+\w+\b`),
		regexp.MustCompile(`(?i)\b(?:prüfe|überprüfe|bestätige|teste|schau dir)\s+\w+\b`),
		// E. Trailing soft asks
		regexp.MustCompile(`(?i)\bwas denkst du\b`),
		regexp.MustCompile(`(?i)\bfeedback\b\s*$`),
	}

	waitingCuePatternsES = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bavísame|avíseme\b`),
		regexp.MustCompile(`(?i)\bdime|dígame\b`),
		regexp.MustCompile(`(?i)\b¿?podrías|¿?podría\b`),
		regexp.MustCompile(`(?i)\besperando tu|esperando su\b`),
		regexp.MustCompile(`(?i)\blisto para tu|listo para su\b`),
		regexp.MustCompile(`(?i)\bantes de que (?:yo|nosotros)\b`),
		regexp.MustCompile(`(?i)\ben cuanto (?:tú|usted)\b`),
		regexp.MustCompile(`(?i)\bespero tu|espero su\b`),
		regexp.MustCompile(`(?i)\bpor favor\s+\w+\b`),
		regexp.MustCompile(`(?i)\b(?:confirma|verifica|revisa|comprueba)\s+\w+\b`),
		regexp.MustCompile(`(?i)\bqué (?:opinas|piensas)\b`),
	}

	waitingCuePatternsFR = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bdis-moi|dites-moi\b`),
		regexp.MustCompile(`(?i)\bfais-moi savoir|faites-le-moi savoir\b`),
		regexp.MustCompile(`(?i)\bpourrais-tu|pourriez-vous\b`),
		regexp.MustCompile(`(?i)\bj'attends ta|j'attends votre\b`),
		regexp.MustCompile(`(?i)\bprêt pour ta|prêt pour votre\b`),
		regexp.MustCompile(`(?i)\bavant que je\b`),
		regexp.MustCompile(`(?i)\bdès que tu\b|\bdès que vous\b`),
		regexp.MustCompile(`(?i)\bs'il te plaît|s'il vous plaît\b`),
		regexp.MustCompile(`(?i)\b(?:confirme|vérifie|teste|relis)\s+\w+\b`),
		regexp.MustCompile(`(?i)\bqu'en penses-tu\b`),
	}

	waitingCuePatternsPT = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bme avise|avisa-me\b`),
		regexp.MustCompile(`(?i)\bme diga|diga-me\b`),
		regexp.MustCompile(`(?i)\bpoderia você|você poderia\b`),
		regexp.MustCompile(`(?i)\baguardo (?:sua|seu|teu|tua)\b`),
		regexp.MustCompile(`(?i)\bpronto para (?:sua|seu|teu|tua) (?:revisão|aprovação)\b`),
		regexp.MustCompile(`(?i)\bantes de eu\b`),
		regexp.MustCompile(`(?i)\bassim que você\b`),
		regexp.MustCompile(`(?i)\bpor favor\s+\w+\b`),
		regexp.MustCompile(`(?i)\b(?:confirme|verifique|revise|teste)\s+\w+\b`),
		regexp.MustCompile(`(?i)\bo que você acha\b`),
	}

	// Japanese and Chinese patterns skip the `(?i)` flag (no case folding)
	// and word boundaries `\b` (CJK scripts have no whitespace-delimited
	// word boundaries for Go's regexp engine to anchor on).
	waitingCuePatternsJA = []*regexp.Regexp{
		regexp.MustCompile(`教えてください`),
		regexp.MustCompile(`知らせてください|お知らせください`),
		regexp.MustCompile(`確認してください`),
		regexp.MustCompile(`確認をお願いします`),
		regexp.MustCompile(`(?:承認|レビュー)をお願いします`),
		regexp.MustCompile(`よろしいですか`),
		regexp.MustCompile(`どう思いますか`),
	}

	waitingCuePatternsZH = []*regexp.Regexp{
		regexp.MustCompile(`请告诉我`),
		regexp.MustCompile(`请(?:确认|检查|核实)`),
		regexp.MustCompile(`请(?:审核|批准|审阅)`),
		regexp.MustCompile(`等待(?:您|你)的(?:回复|确认|批准)`),
		regexp.MustCompile(`你觉得怎么样|您觉得如何`),
	}
)

// allWaitingCuePatterns is the full multilingual set ExtractWaitingCue
// scans against — the English set (waitingCuePatterns, issue #381) plus the
// per-language buckets above (issue #933).
var allWaitingCuePatterns = concatWaitingCuePatterns()

func concatWaitingCuePatterns() []*regexp.Regexp {
	all := make([]*regexp.Regexp, 0,
		len(waitingCuePatterns)+
			len(waitingCuePatternsDE)+len(waitingCuePatternsES)+
			len(waitingCuePatternsFR)+len(waitingCuePatternsPT)+
			len(waitingCuePatternsJA)+len(waitingCuePatternsZH))
	all = append(all, waitingCuePatterns...)
	all = append(all, waitingCuePatternsDE...)
	all = append(all, waitingCuePatternsES...)
	all = append(all, waitingCuePatternsFR...)
	all = append(all, waitingCuePatternsPT...)
	all = append(all, waitingCuePatternsJA...)
	all = append(all, waitingCuePatternsZH...)
	return all
}

// i18nAnswerPrefixes extend answerPrefixes (the rhetorical-question veto,
// issue #236) with the top languages' "because/since" equivalents, so a
// rhetorical Q&A pair in those languages is also correctly skipped rather
// than misread as a real waiting question.
var i18nAnswerPrefixes = []string{
	// German
	"weil ", "weil,", "da ",
	// Spanish / Portuguese share "porque"
	"porque ", "porque,",
	// French
	"parce que ", "car ", "car,",
	// Japanese
	"なぜなら",
	// Chinese
	"因为",
}
