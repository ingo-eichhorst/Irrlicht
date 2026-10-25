package opencode

// IsServiceArgv reports whether argv is opencode 2.x's background service —
// the one process per user that hosts its sessions, started as
// `<exe> serve --service` (#2105). It is Process.SessionHostArgv, so the
// scanner mints no placeholder row for it: the service chdirs to $HOME, and on
// main it showed as a `proc-<pid>` row there.
//
// The shape is read out of the opencode 2.1.4 bundle with `strings` (never by
// running the binary — a 2.x run outside its sandbox migrates the 1.x store in
// place): `service.ensure` spawns `[...oj(), "serve", "--service"]`, oj() is
// `[process.execPath]` for the compiled binary (comm `opencode`), and the
// fallback command is the literal `["opencode","serve","--service"]`. `serve`'s
// own params are hostname, port, cors, and the booleans service and stdio.
//
// `serve` must be the subcommand — the first positional token after argv[0],
// skipping the values of value-taking options — and `--service` must follow it
// before any `--`. So the prompt of `opencode run serve --service`, and
// `opencode --prompt serve --service`, are clients, not the service; and a bare
// `opencode serve` (1.x's headless server) is unchanged. nil or empty argv is
// false.
func IsServiceArgv(argv []string) bool {
	sub, rest := subcommandOf(argv)
	if sub != "serve" {
		return false
	}
	for _, arg := range rest {
		switch arg {
		case "--":
			return false
		case "--service":
			return true
		}
	}
	return false
}

// subcommandOf returns argv's first positional token after argv[0] and the
// tokens after it, or "" when there is none.
func subcommandOf(argv []string) (string, []string) {
	for i := 1; i < len(argv); i++ {
		switch arg := argv[i]; {
		case arg == "--":
			return "", nil
		case len(arg) > 0 && arg[0] == '-':
			if valueOptions[arg] {
				i++ // the next token is this option's value, not a positional
			}
		default:
			return arg, argv[i+1:]
		}
	}
	return "", nil
}

// valueOptions are the opencode 2.1.4 options that can precede the subcommand
// and take their value as the NEXT token, read from the same bundle: the root
// command's text flags `--server`, `--session`/`-s` and `--prompt`, and the
// CLI framework's built-ins `--log-level` and `--completions`. Every other
// root flag there is boolean (--standalone, --auto, --yolo,
// --dangerously-skip-permissions, --continue/-c, --print-logs, --help,
// --version/-v, --wizard). A `--flag=value` token carries its own value and
// needs no entry.
var valueOptions = map[string]bool{
	"--server":      true,
	"--session":     true,
	"-s":            true,
	"--prompt":      true,
	"--log-level":   true,
	"--completions": true,
}
