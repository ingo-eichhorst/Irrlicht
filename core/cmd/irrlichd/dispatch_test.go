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
//   - "an unknown flag still starts the daemon". That is the behaviour every
//     irrlichd has had, and the #1373 change deliberately narrows the new
//     rejection to POSITIONAL tokens so it cannot break an existing invocation.
//     It is a LOCK: it passes on main by construction and exists so a later
//     tightening of the parser has to be a deliberate decision.
func TestSelectActionOrder(t *testing.T) {
	tests := map[string]struct {
		args []string
		want cliAction
	}{
		"no arguments starts the daemon":            {nil, actionRunDaemon},
		"--record starts the daemon":                {[]string{"--record"}, actionRunDaemon},
		"an unknown flag still starts the daemon":   {[]string{"--not-a-real-flag"}, actionRunDaemon},
		"--version prints the version":              {[]string{"--version"}, actionVersion},
		"-v prints the version":                     {[]string{"-v"}, actionVersion},
		"--uninstall-hooks":                         {[]string{"--uninstall-hooks"}, actionUninstallHooks},
		"--print-managed-files":                     {[]string{"--print-managed-files"}, actionPrintManagedFiles},
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
