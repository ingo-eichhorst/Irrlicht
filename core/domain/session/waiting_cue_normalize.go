package session

import "strings"

// combiningDiacriticMarks are the Unicode "combining diacritical marks"
// (NFD) this package composes back onto their preceding base letter before
// pattern matching. See issue #933 code review: the i18n cue/answer-prefix
// patterns are written with precomposed (NFC) accented characters (é, ç, ã,
// ...); Go's regexp package does no Unicode normalization, so if a
// transcript source ever emitted NFD text — "e" + U+0301 COMBINING ACUTE
// ACCENT instead of the single precomposed rune "é" — every accented
// pattern would silently fail to match.
//
// This composes only the specific base+mark combinations the es/fr/pt
// patterns actually use, rather than pulling in golang.org/x/text/unicode/norm
// for full NFC normalization: that package would ratchet several unrelated
// transitive dependency versions across the whole workspace (go.work.sum is
// shared by every module in this repo) just to defend against an edge case
// that hasn't been observed in practice — LLM output is effectively always
// NFC already.
var combiningDiacriticMarks = map[rune]map[rune]rune{
	0x0300: {'a': 'à', 'e': 'è', 'i': 'ì', 'o': 'ò', 'u': 'ù', 'A': 'À', 'E': 'È', 'I': 'Ì', 'O': 'Ò', 'U': 'Ù'},
	0x0301: {'a': 'á', 'e': 'é', 'i': 'í', 'o': 'ó', 'u': 'ú', 'A': 'Á', 'E': 'É', 'I': 'Í', 'O': 'Ó', 'U': 'Ú'},
	0x0302: {'a': 'â', 'e': 'ê', 'i': 'î', 'o': 'ô', 'u': 'û', 'A': 'Â', 'E': 'Ê', 'I': 'Î', 'O': 'Ô', 'U': 'Û'},
	0x0303: {'a': 'ã', 'n': 'ñ', 'o': 'õ', 'A': 'Ã', 'N': 'Ñ', 'O': 'Õ'},
	0x0308: {'a': 'ä', 'e': 'ë', 'i': 'ï', 'o': 'ö', 'u': 'ü', 'A': 'Ä', 'E': 'Ë', 'I': 'Ï', 'O': 'Ö', 'U': 'Ü'},
	0x0327: {'c': 'ç', 'C': 'Ç'},
}

// combiningDiacriticMarksCutset is combiningDiacriticMarks' keys as a string,
// for the cheap strings.ContainsAny prefilter in foldCombiningDiacritics.
const combiningDiacriticMarksCutset = "̧̀́̂̃̈"

// foldCombiningDiacritics composes NFD base-letter+combining-mark pairs
// found in combiningDiacriticMarks into their precomposed rune, leaving
// everything else (including base+mark combinations not in the table)
// untouched.
func foldCombiningDiacritics(s string) string {
	if !strings.ContainsAny(s, combiningDiacriticMarksCutset) {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if i+1 < len(runes) {
			if marks, ok := combiningDiacriticMarks[runes[i+1]]; ok {
				if composed, ok := marks[r]; ok {
					out = append(out, composed)
					i++
					continue
				}
			}
		}
		out = append(out, r)
	}
	return string(out)
}
