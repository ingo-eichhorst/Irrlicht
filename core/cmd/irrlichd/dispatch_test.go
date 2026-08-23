package main

import "testing"

// TestSelectActionOrder pins irrlichd's argv dispatch, which until #1373 was an
// unparsed chain of exact-match flag scans falling through to runDaemon().
//
// Two rows carry the weight, and both fail silently rather than loudly:
//
//   - "the guarded form we install". Every installed beacon command line LEADS
//     with --version, so that an irrlichd predating this subcommand no-ops
//     instead of starting a daemon — and leads rather than trails because
//     releases up to v0.3.2 only ever matched os.Args[1]
//     (hookbeacon.LegacyGuardToken records the git evidence). If the version
//     check were ever moved back in front of the verb, every installed beacon
//     would become a version banner: exit 0, nothing delivered, no error
//     anywhere, and session state would simply stop arriving.
//   - "an unknown flag is rejected". This row was the inverse until #1417 — it
//     asserted that an unknown flag STARTS THE DAEMON, the behaviour every
//     irrlichd had had, and it was written as a LOCK so that a later tightening
//     of the parser would have to be a deliberate decision rather than a
//     side effect. #1417 is that deliberate decision, and flipping this row is
//     the visible form of it: `irrlichd --recrod` now exits 2 naming --recrod
//     instead of silently starting a daemon with recording off. The lock did its
//     job — it made the change reviewable — so what replaces it is a lock in the
//     other direction, and the call-site sweep in the #1417 PR is the evidence
//     that no real invocation relied on the old fall-through.
func TestSelectActionOrder(t *testing.T) {
	tests := map[string]struct {
		args []string
		want cliAction
	}{
		"no arguments starts the daemon":            {nil, actionRunDaemon},
		"--record starts the daemon":                {[]string{"--record"}, actionRunDaemon},
		"an unknown flag is rejected":               {[]string{"--not-a-real-flag"}, actionUnknownFlag},
		"a typo of a known flag is rejected":        {[]string{"--recrod"}, actionUnknownFlag},
		"a value form of a boolean flag":            {[]string{"--record=1"}, actionUnknownFlag},
		"an unknown flag beats a known one":         {[]string{"--diagnose", "--recrod"}, actionUnknownFlag},
		"an empty argument is not a flag":           {[]string{""}, actionRunDaemon},
		"--version prints the version":              {[]string{"--version"}, actionVersion},
		"-v prints the version":                     {[]string{"-v"}, actionVersion},
		"--uninstall-hooks":                         {[]string{"--uninstall-hooks"}, actionUninstallHooks},
		"--print-managed-files":                     {[]string{"--print-managed-files"}, actionPrintManagedFiles},
		"--print-advisory-files":                    {[]string{"--print-advisory-files"}, actionPrintAdvisoryFiles},
		"--uninstall-task-eta":                      {[]string{"--uninstall-task-eta"}, actionUninstallTaskEta},
		"--diagnose":                                {[]string{"--diagnose"}, actionDiagnose},
		"the beacon verb":                           {[]string{"hook-post", "gemini-cli"}, actionBeacon},
		"the guarded form we install":               {[]string{"--version", "hook-post", "gemini-cli"}, actionBeacon},
		"the guarded form beats the version branch": {[]string{"--version", "hook-post", "gemini-cli", ">/dev/null"}, actionBeacon},
		"a trailing guard token is still the verb":  {[]string{"hook-post", "gemini-cli", "--version"}, actionBeacon},
		"the beacon verb with no adapter":           {[]string{"hook-post"}, actionBeacon},
		"the beacon verb with a shell redirect":     {[]string{"hook-post", "gemini-cli", ">/dev/null"}, actionBeacon},
		"a positional that is not a subcommand":     {[]string{"start"}, actionUnknownSubcommand},
		"the beacon verb in a non-leading position": {[]string{"--record", "hook-post"}, actionUnknownSubcommand},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := selectAction(tt.args); got != tt.want {
				t.Errorf("selectAction(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestUnknownSubcommandNeverReachesTheDaemon states the hazard positively: a
// beacon-shaped invocation on a future irrlichd that renamed the verb must not
// start a daemon. A stray start is not merely wasteful — setupUnixSocket removes
// the socket file before listening, so it breaks the live daemon's socket on its
// way to failing to bind the port.
func TestUnknownSubcommandNeverReachesTheDaemon(t *testing.T) {
	for _, args := range [][]string{
		{"hook-post-v2", "gemini-cli"},
		{"post-hook", "codex", "--version"},
		{"gemini-cli"},
	} {
		if got := selectAction(args); got == actionRunDaemon {
			t.Errorf("selectAction(%q) = actionRunDaemon; an unrecognized subcommand must never start a daemon", args)
		}
	}
}

// TestUnknownFlagNeverStartsTheDaemon is #1417's defect test. Until this ticket
// an unknown FLAG fell through to runDaemon(), so `irrlichd --recrod` started a
// daemon with recording off and said nothing — the worst shape available,
// because it looks like success.
func TestUnknownFlagNeverStartsTheDaemon(t *testing.T) {
	for _, args := range [][]string{
		{"--recrod"},              // the issue's literal typo
		{"--not-a-real-flag"},     // #1412's row, now inverted
		{"--record=1"},            // a value form of a boolean flag: silently ignored before
		{"--record", "--diagnos"}, // a typo alongside a good flag
		{"--uninstall-hook"},      // a near-miss of a destructive flag
	} {
		if got := selectAction(args); got == actionRunDaemon {
			t.Errorf("selectAction(%q) = actionRunDaemon; an unknown flag must never start a daemon", args)
		}
	}
}

func TestFirstPositional(t *testing.T) {
	tests := map[string]struct {
		args []string
		want string
		ok   bool
	}{
		"only flags":            {[]string{"--record", "-v"}, "", false},
		"a positional":          {[]string{"--record", "start"}, "start", true},
		"empty strings skipped": {[]string{"", "start"}, "start", true},
		"nothing":               {nil, "", false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := firstPositional(tt.args)
			if got != tt.want || ok != tt.ok {
				t.Errorf("firstPositional(%q) = %q, %v; want %q, %v", tt.args, got, ok, tt.want, tt.ok)
			}
		})
	}
}
