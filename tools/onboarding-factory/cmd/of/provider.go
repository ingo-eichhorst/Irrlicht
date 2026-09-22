package main

import (
	"fmt"
	"io"

	"irrlicht/tools/onboarding-factory/internal/provider"
)

// The `provider` domain reads replaydata/providers/ — the BILLING-PRODUCT
// catalog, which #1977 §8 and issue #2008 §1.5 keep deliberately apart from
// the agent × scenario matrix the rest of this command reads. `of status` and
// `of verify` answer questions about agent adapters; `of provider status` and
// `of provider verify` answer questions about billed products, with their own
// vocabulary (internal/provider's four claim states, not the maturity ladder).
//
// Two verbs only. #2008 §1.2 is explicit that `add`, `assess`, `probe` and
// `record` wait for demonstrated onboarding need rather than shipping as an
// empty framework.
const providerUsage = `usage:
  of provider verify [--json] [--repo-root .]
  of provider status [--json] [--repo-root .]`

func runProvider(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, providerUsage)
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return runProviderVerify(args[1:], stdout, stderr)
	case "status":
		return runProviderStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "of provider: verb must be verify or status")
		return exitUsage
	}
}

// providerRequest is the flag set both verbs share.
type providerRequest struct {
	RepoRoot string
	JSON     bool
}

func parseProviderRequest(name string, args []string) (providerRequest, bool) {
	fs := newFlagSet(name)
	asJSON := fs.Bool("json", false, "emit JSON")
	repoRoot := fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return providerRequest{}, false
	}
	// absRoot at the flag boundary, for the reason its own doc comment gives:
	// the filesystem readers answer a ".."-bearing path with an EMPTY result,
	// which would render here as a provider catalog in which nothing is
	// recorded — exit 0, no warning.
	return providerRequest{RepoRoot: absRoot(*repoRoot), JSON: *asJSON}, true
}

// runProviderVerify checks the manifest schema, the presence and top-level
// shape of every fixture and cited path, and the shared obligations #2003 and
// #2007 established. It does not run a fixture through a provider parser --
// "verify" here means the data is well-formed and the claims are earned.
//
// An ABSENT replaydata/providers/ is a failure here, unlike in `of validate`:
// this command was asked about providers, so "there is no tree" answers the
// question it was asked and must not be reported as a clean run.
func runProviderVerify(args []string, stdout, stderr io.Writer) int {
	req, ok := parseProviderRequest("of provider verify", args)
	if !ok {
		return exitUsage
	}
	findings := provider.ValidateRepo(req.RepoRoot)
	if req.JSON {
		out := map[string]any{"ok": len(findings) == 0, "findings": emptyIfNil(findings)}
		_ = writeJSON(stdout, out)
	} else if len(findings) == 0 {
		fmt.Fprintln(stdout, "of provider verify: OK — every provider manifest is schema-valid, every fixture and cited path is present, and no claim outruns its evidence")
	} else {
		fmt.Fprintf(stderr, "of provider verify: %d violation(s):\n", len(findings))
		for _, f := range findings {
			fmt.Fprintf(stderr, "  %s: %s\n", f.Path, f.Message)
		}
	}
	if len(findings) > 0 {
		return exitFail
	}
	return exitOK
}

// emptyIfNil keeps a JSON `findings` key an array rather than null, matching
// `of validate --json`.
func emptyIfNil(f []provider.Finding) []provider.Finding {
	if f == nil {
		return []provider.Finding{}
	}
	return f
}

// runProviderStatus renders claimed-versus-earned per axis. It exits non-zero
// on a gap so the report is a gate rather than a reading: #2008's completion
// criterion is that an unearned claim is "reported, not believed".
func runProviderStatus(args []string, stdout, stderr io.Writer) int {
	req, ok := parseProviderRequest("of provider status", args)
	if !ok {
		return exitUsage
	}
	report := provider.Status(req.RepoRoot)
	if req.JSON {
		report.Findings = emptyIfNil(report.Findings)
		_ = writeJSON(stdout, report)
	} else {
		printProviderStatus(stdout, stderr, report)
	}
	if report.Gaps > 0 || len(report.Findings) > 0 {
		return exitFail
	}
	return exitOK
}

// providerRowFormat is the one format string the header and every row share,
// so the columns cannot drift apart.
const providerRowFormat = "%-12s %-18s %-12s %-18s %-28s %-28s %s\n"

func printProviderStatus(stdout, stderr io.Writer, report provider.StatusReport) {
	fmt.Fprintf(stdout, "billing providers — %d, %d capability gap(s)\n\n", len(report.Providers), report.Gaps)
	// The column headers ARE the schema tokens, so a renamed field renames its
	// own header instead of leaving a stale one behind.
	fmt.Fprintf(stdout, providerRowFormat, "provider", "strategy", "impl", "axis", "claimed", "earned", "gap")
	for _, p := range report.Providers {
		printProviderRows(stdout, p)
	}
	if len(report.Findings) > 0 {
		fmt.Fprintf(stderr, "\nof provider status: %d finding(s) — the catalog could not be read in full:\n", len(report.Findings))
		for _, f := range report.Findings {
			fmt.Fprintf(stderr, "  %s: %s\n", f.Path, f.Message)
		}
	}
}

func printProviderRows(stdout io.Writer, p provider.ProviderStatus) {
	for i, ax := range p.Axes {
		id, strategy, impl := p.ID, p.Strategy, p.Implementation
		if i > 0 {
			// Only the first row of a provider repeats its identity; a blank
			// cell here is unambiguous because the provider column is sorted.
			id, strategy, impl = "", "", ""
		}
		gap := ""
		if ax.Gap {
			gap = "GAP — " + ax.Reason
		}
		fmt.Fprintf(stdout, providerRowFormat, id, strategy, impl, ax.Axis, ax.Claimed, ax.Earned, gap)
	}
}
