package provider

import (
	"fmt"
	"sort"
)

// AxisStatus is one axis's claimed state next to the state its cited evidence
// earns, plus the gap between them when there is one.
type AxisStatus struct {
	Axis    string `json:"axis"`
	Claimed string `json:"claimed"`
	Earned  string `json:"earned"`
	Gap     bool   `json:"gap"`
	Reason  string `json:"reason,omitempty"`
}

// ProviderStatus is one provider's row.
type ProviderStatus struct {
	ID             string       `json:"id"`
	DisplayName    string       `json:"display_name"`
	Strategy       string       `json:"strategy"`
	Implementation string       `json:"implementation"`
	Axes           []AxisStatus `json:"axes"`
	Gaps           int          `json:"gaps"`
}

// StatusReport is what `of provider status` renders or encodes.
type StatusReport struct {
	Providers []ProviderStatus `json:"providers"`
	Findings  []Finding        `json:"findings"`
	Gaps      int              `json:"gaps"`
}

// Status reads the tree and reports claimed-versus-earned per axis.
//
// Findings carries the read-side failures (an unreadable tree, an unparseable
// manifest). They are reported rather than rendered as an empty table, because
// an empty table reads as a finished measurement.
func Status(repoRoot string) StatusReport {
	res := load(repoRoot)
	report := StatusReport{Findings: res.Findings}
	for _, p := range res.Providers {
		row := statusOf(repoRoot, p)
		report.Gaps += row.Gaps
		report.Providers = append(report.Providers, row)
	}
	sort.Slice(report.Providers, func(i, j int) bool { return report.Providers[i].ID < report.Providers[j].ID })
	sortFindings(report.Findings)
	return report
}

func statusOf(repoRoot string, p Loaded) ProviderStatus {
	m := p.Manifest
	row := ProviderStatus{
		ID:             p.ID,
		DisplayName:    m.DisplayName,
		Strategy:       m.Observation.Strategy,
		Implementation: m.Observation.Implementation.Kind,
	}
	// Evidence whose ref is not on disk is dropped here rather than counted.
	// A citation naming a file that is gone is an inability to look, and this
	// report exits non-zero, so letting it keep earning its rank would make
	// "nothing to find" and "could not look" print identically. ValidateRepo
	// reports the absent ref separately, through verifyEvidence.
	byID := map[string]EvidenceEntry{}
	for _, e := range m.Evidence {
		if evidenceResolves(repoRoot, e) {
			byID[e.ID] = e
		}
	}
	for _, axis := range AxisIDs() {
		as := axisStatus(axis, m.Capabilities[axis], byID)
		if as.Gap {
			row.Gaps++
		}
		row.Axes = append(row.Axes, as)
	}
	return row
}

// axisStatus derives what one axis's cited evidence actually earns.
//
// The rule that matters is #1977 §11.11 — "Provider status cannot claim live
// verification from a fixture alone": live-verified is earned only by a
// live-probe or a recording, never by a shape fixture or a source citation.
func axisStatus(axis string, c Capability, byID map[string]EvidenceEntry) AxisStatus {
	out := AxisStatus{Axis: axis, Claimed: c.Claim}
	cited := citedEvidence(c, byID)
	out.Earned = earnedFrom(cited)

	if !IsValidClaim(c.Claim) {
		// claimRank answers -1 for an unknown token, so the rank comparison
		// below would read a claim outside the vocabulary as "not above what
		// it earns" -- i.e. as no gap at all. Reported here instead, because
		// a state nobody defined is the last one to take on trust.
		out.Gap = true
		out.Reason = fmt.Sprintf("%q is not one of: %s", c.Claim, oneOf(ClaimStates))
		return out
	}
	if c.Claim == ClaimSourceUnavailable {
		// Off-ladder: this is a positive finding about a negative result, so
		// it is not compared by rank. It still has to name its evidence —
		// #1977 §5, "Negative findings must name the route, version, command
		// or file".
		out.Earned = ClaimSourceUnavailable
		// A source citation alone cannot establish that a ROUTE reports
		// nothing: reading a parser tells you what the parser handles, not
		// what the endpoint answered. docs/providers/catalog.md says the same
		// of the census -- source-verified "is a label for evidence quality,
		// not one of the four result states".
		if !hasObservationalEvidence(cited) {
			out.Gap = true
			out.Earned = ClaimUnassessed
			out.Reason = "cites no observation; a route that reports nothing still has to say where that was observed, " +
				"and a source citation alone does not observe a route"
		}
		return out
	}
	if claimRank(c.Claim) > claimRank(out.Earned) {
		out.Gap = true
		out.Reason = gapReason(c.Claim, cited)
	}
	return out
}

func citedEvidence(c Capability, byID map[string]EvidenceEntry) []EvidenceEntry {
	var out []EvidenceEntry
	for _, id := range c.Evidence {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

// earnedFrom maps cited evidence to the highest state it supports.
//
// EvidenceSource earns NOTHING on its own, which is the whole distinction
// docs/providers/catalog.md draws: reading a pinned source at a revision is
// "source-verified", an evidence-quality label, and never one of the four
// result states. A source citation is still worth carrying -- it says where
// the shape came from -- it just cannot be what a claim rests on.
func earnedFrom(cited []EvidenceEntry) string {
	earned := ClaimUnassessed
	for _, e := range cited {
		if inSet(e.Kind, liveEvidenceKinds) {
			return ClaimLiveVerified
		}
		if e.Kind == EvidenceFixture {
			earned = ClaimFixtureVerified
		}
	}
	return earned
}

// hasObservationalEvidence reports whether any citation observed the route
// itself, rather than describing it.
func hasObservationalEvidence(cited []EvidenceEntry) bool {
	for _, e := range cited {
		if e.Kind != EvidenceSource && inSet(e.Kind, EvidenceKinds) {
			return true
		}
	}
	return false
}

func gapReason(claim string, cited []EvidenceEntry) string {
	if len(cited) == 0 {
		return fmt.Sprintf("claims %s and cites no evidence", claim)
	}
	kinds := make([]string, 0, len(cited))
	for _, e := range cited {
		kinds = append(kinds, e.Kind)
	}
	return fmt.Sprintf("claims %s but its evidence is %s; %s is earned only by %s, and %s by %s",
		claim, oneOf(kinds), ClaimLiveVerified, oneOf(liveEvidenceKinds),
		ClaimFixtureVerified, EvidenceFixture)
}

// statusGapFindings turns every claimed-but-unearned axis into a validation
// finding, so `of validate` fails on an unearned claim the same way it already
// fails on an adapter claiming a maturity tier its core standing has not
// earned. Reporting the gap rather than believing the claim is issue #2008's
// completion criterion.
func statusGapFindings(repoRoot string, providers []Loaded) []Finding {
	var out []Finding
	for _, p := range providers {
		for _, as := range statusOf(repoRoot, p).Axes {
			if !as.Gap {
				continue
			}
			out = append(out, Finding{
				Path: p.RelDir + "/" + ManifestFile,
				Message: fmt.Sprintf("capability %q claims %q but earns %q: %s",
					as.Axis, as.Claimed, as.Earned, as.Reason),
			})
		}
	}
	return out
}
