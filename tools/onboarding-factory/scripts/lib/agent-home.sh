#!/usr/bin/env bash
# agent-home.sh — the recording rig's per-adapter "agent home" isolation, owned
# once instead of once per script.
#
# Several agents resolve their WHOLE config directory — session store, settings
# and (for a hook-installing adapter) the hook config the daemon writes — from a
# single env var. Pointing that var at a scratch directory is what keeps a
# recording off the operator's real ~/.codex, ~/.copilot, ~/.kiro or ~/.vibe.
#
# WHAT THIS IS NOT FOR. The rig already protects the user's declared agent
# configs unconditionally: lib/managed-file-snapshot.sh asks the daemon for
# `--print-managed-files`, backs every one of them up before the daemon spawns,
# and hands them back from an EXIT trap — and spawn_record_daemon REFUSES to
# start if that snapshot fails. So this file is not a data-loss guard and must
# not be described as one. What it buys is narrower and still real:
#
#   - The session store. Without it the daemon watches the operator's REAL
#     session directory, so their own live sessions can land in the fixture and
#     the recorded session lands in their store. That is why copilot defaults.
#   - Crash safety. Restore runs from an EXIT trap, so SIGKILL or a panic leaves
#     the real configs rewritten. An isolated home has nothing to hand back.
#   - Reproducibility. A run starts from a known-empty (or deliberately seeded)
#     home rather than from whatever the operator's looks like today.
#
# THE FAILURE THIS FILE ACTUALLY REMOVES is not any of those three — it is that
# the lever was HALF-WIRED and failed silently. run-cell.sh exports the var, so
# the daemon (a direct child) inherits it, while the agent CLI is launched by
# the driver through `tmux new-session` and therefore inherits the tmux SERVER's
# environment. Measured on this machine (private `-L` socket, so no real session
# was touched): against a server started without the variable, a pane created by
# a shell that exported it reads `<UNSET>`; the same server with an explicit
# `env VAR=… ` prefix on the new-session command line reads the value. A dev
# machine essentially always has a tmux server already running, so setting the
# var got you a daemon watching the scratch home and a CLI writing to the real
# one — an empty fixture, with nothing anywhere saying why. Every driver that
# honours a row here therefore prefixes `env VAR=…` on its own tmux launch;
# codex's driver already did, and that is the shape the others copy.
#
# Usage — one call, before the daemon spawns, so the daemon inherits the value
# and `--print-managed-files` resolves under it:
#   source "$SCRIPT_DIR/lib/agent-home.sh"
#   agent_home_isolate "$ADAPTER" "$STAGING/agent-home" || exit 1

# agent_home_table prints the declaration, one row per adapter:
#
#   <adapter> <ENV_VAR> <policy>
#
# It is a heredoc rather than an array because macOS ships bash 3.2, where
# `declare -A` is a hard runtime error (run-cell-multi.sh says the same at its
# own slot bookkeeping). It is also the single source the Go tripwire reads —
# TestRigHomeTableMatchesTheAdapterRegistry
# (tools/onboarding-factory/internal/righome) SOURCES this file and runs this
# function rather than parsing it, so the two cannot drift.
#
# POLICY — the distinction is load-bearing and predates this file:
#
#   default  The var is set to a staging directory when the caller leaves it
#            unset. Only copilot. Safe to default because Copilot's credentials
#            live in the OS keychain, not in COPILOT_HOME, so a scratch home
#            needs no re-login (verified on CLI 1.0.77 — see the copilot
#            driver's seed_copilot_home).
#
#   optin    Nothing happens unless the caller exports an absolute path, which
#            the operator is expected to have SEEDED first. Everything else.
#
#            The general rule is NOT "credentials", which is how codex's block
#            phrased it and which is too narrow: it is that a scratch home
#            starts without some piece of state the run needs, and every way of
#            missing it ends in a blocking interactive prompt inside the tmux
#            pane that no driver has an arm for — so the cell stalls to timeout
#            and records nothing. A credential is one such piece of state. The
#            per-adapter reasons below are NOT interchangeable and are recorded
#            individually, because two of them are not credentials at all:
#
#     codex        CREDENTIALS. auth.json lives in CODEX_HOME, so defaulting it
#                  to an empty staging dir would leave every codex recording
#                  silently unauthenticated. Seed the scratch home with a copy
#                  of auth.json (#1388).
#     mistral-vibe THE PROVIDER DECLARATION, which is upstream of the
#                  credential. It is tempting to read config.toml's
#                  `api_key_env_var` as "the key is in an env var, so a scratch
#                  home loses nothing" — that is true of the SECRET and false of
#                  the lookup: `[[providers]]` (api_base, api_key_env_var,
#                  backend) lives in $VIBE_HOME/config.toml, and
#                  resolve_api_key(env_key) (v2.19.1
#                  vibe/core/config/_settings.py) reads the process env and then
#                  the OS keyring UNDER THAT NAME. A home with no config.toml
#                  falls back to DEFAULT_PROVIDERS, so an operator on any
#                  non-default provider has the wrong name looked up, raises
#                  MissingAPIKeyError, and cli.py:92 answers an interactive
#                  session by running the onboarding WIZARD in the pane.
#                  Separately, $VIBE_HOME/.env is a real credential location in
#                  its own right — persist_api_key
#                  (vibe/setup/auth/api_key_persistence.py) writes the key there
#                  when the keyring raises, and paths/_vibe_home.py declares
#                  GLOBAL_ENV_FILE = VIBE_HOME/".env" — so on a keyring-less
#                  host, or under VIBE_TEST_DISABLE_KEYRING=1, the secret IS in
#                  this directory. Seed config.toml, and .env if you have one.
#                  NOT trusted_folders.toml, which also lives here and looks
#                  like it should matter: the driver passes `--trust` on every
#                  launch already, because the staging cwd lives under the
#                  irrlicht git repo and is untrusted whichever home is in use
#                  (see launch_repl in the mistral-vibe driver). Relocating the
#                  home loses nothing there that a fresh cwd had not lost.
#                  ONE MORE SEEDING GOTCHA, measured in #1735 and invisible
#                  until a run has already been spent.
#                  [session_logging].save_dir in config.toml is an ABSOLUTE path
#                  and OVERRIDES the VIBE_HOME-derived default, so a config.toml
#                  copied from the operator's real home relocates everything
#                  EXCEPT the transcripts — vibe wrote three sessions into the
#                  real ~/.vibe/logs/session while the driver waited on an empty
#                  scratch one and timed out. Point it inside the scratch home
#                  or delete the line; the driver reads the key and refuses
#                  rather than recording half-isolated.
#                  A real config.toml used to DEFEAT the hooks install outright
#                  (#1753): hooktoml's splicer refused any document containing
#                  a multi-line array, vibe writes twelve of them by default
#                  (applied_migrations plus the [tools.*] allow/deny lists), and
#                  Apply then failed with "contains a construct this splicer
#                  does not model safely" — permission still read granted, and
#                  the refusal surfaced only in unapplied_grants. #1753 is fixed
#                  (#1755, hooktoml models multi-line arrays), so the seed is a
#                  real, unedited config.toml — no collapsing.
#     pi           CREDENTIALS, the same rule codex's row states. pi resolves
#                  every agent-dir path from getAgentDir()
#                  (dist/config.js) — sessions/, settings.json, extensions/
#                  AND auth.json — so PI_CODING_AGENT_DIR relocates the store,
#                  the hook install and the credentials together. That
#                  togetherness is what makes the row honest where claudecode's
#                  would not be (see below): irrlicht's own pi install writes
#                  ~/.pi/agent/extensions/irrlicht.js, resolved through the
#                  SAME override (pi/hookinstaller.go's piAgentDir), so an
#                  isolated home isolates the extension too. It also means an
#                  isolated home starts with no auth.json, hence optin rather
#                  than default.
#     kiro-cli     PERSISTED TRUST-ALL CONSENT — and note this is the one row
#                  where the credential rule says "safe to default" and the
#                  answer is still no. kiro-cli's token is at
#                  ~/.aws/sso/cache/kiro-auth-token-cli.json, which follows $HOME
#                  and not $KIRO_HOME, and the real ~/.kiro holds no credential
#                  material at all (agents/, argv.json, extensions/, logs/,
#                  powers/, sessions/, settings/, skills/, steering/). What it
#                  DOES hold is settings/cli.json, whose
#                  chat.disableTrustAllConfirmation is the persisted consent the
#                  kiro driver states it depends on — "kiro-cli launches with
#                  persisted --trust-all-tools consent, so a fresh cwd never
#                  stalls on a trust picker" (step_restart's own comment). A
#                  scratch home has no such file. Seed settings/cli.json.
#                  Whether kiro-cli actually blocks there is NOT verified —
#                  doing so means driving the live CLI — so this row is opt-in
#                  on the strength of the driver's stated premise no longer
#                  holding, which is the honest reason rather than a measured
#                  one. Defaulting it would also change the recording conditions
#                  of all 28 existing kiro-cli recordings at once.
#
#   hermes     OPT-IN, for kiro-cli's reason in a stronger form. HERMES_HOME is
#              the one variable that moves EVERYTHING — config.yaml, the
#              shell-hook allowlist and state.db all hang off it, and the
#              adapter's own homeDir() reads it — so unlike opencode there is
#              no half-relocated state to fall into. What stops it defaulting is
#              that a bare Hermes home cannot authenticate: #1325's recording
#              work measured five files it has to be seeded with (config.yaml,
#              .env, auth.json, context_length_cache.yaml,
#              provider_models_cache.json) before `hermes chat` will start.
#              Defaulting it would also change the recording conditions of all
#              22 existing hermes recordings at once.
#
#            A "default AND seed from the real home" third policy was considered
#            and not built: it would copy the operator's own config into staging
#            on every run, which trades an explicit, auditable operator step for
#            an implicit read of their real home, and it cannot be tested here
#            without a live CLI to prove the seeded home actually boots.
#
# NOT IN THE TABLE, and why — both are structural, neither is a to-do:
#
#   claudecode   HALF-relocatable, which is worse than not at all. Its
#                transcripts follow CLAUDE_CONFIG_DIR
#                (claudecode/adapter.go transcriptsDir) but its hook config does
#                NOT — claudeSettingsPath (claudecode/hookinstaller.go) joins
#                os.UserHomeDir() with ".claude/settings.json" unconditionally.
#                A row here would move the session store while the install kept
#                landing in the operator's real settings.json, i.e. it would
#                claim an isolation it does not have.
#   gemini-cli   No override exists at all. defaultRootDir is ".gemini/tmp"
#                under $HOME and geminiHome() is $HOME/.gemini; there is no
#                os.Getenv anywhere in core/adapters/inbound/agents/geminicli.
#                Its hook recordings can only run against the real home, with
#                the managed-file snapshot as the whole of the protection.
#   opencode     SPLIT ACROSS TWO XDG VARIABLES, which a one-variable row
#                cannot express. opencode resolves its config dir — where
#                irrlicht installs its plugin — from $XDG_CONFIG_HOME, and its
#                DATA dir — the SQLite store the daemon watches — from
#                $XDG_DATA_HOME. A row naming the first relocates the install
#                while the store stays real (claudecode's problem in mirror
#                image); a row naming the second is worse, because irrlicht's
#                own StorePath() joins $HOME with .local/share/opencode
#                unconditionally, so the CLI would move and the daemon would
#                keep watching the operator's real database. Closing it needs
#                BOTH an XDG_DATA_HOME-aware StorePath and a two-variable row
#                shape here (#1719).
#
# Those three are named in righome.Unisolatable with the same reasons, and the
# tripwire fails if a hooks-declaring adapter is in neither place.
agent_home_table() {
  cat <<'TABLE'
copilot COPILOT_HOME default
codex CODEX_HOME optin
kiro-cli KIRO_HOME optin
mistral-vibe VIBE_HOME optin
pi PI_CODING_AGENT_DIR optin
hermes HERMES_HOME optin
TABLE
  return 0
}

# agent_home_field <adapter> <column-number> prints one field of an adapter's
# row, or nothing when the adapter has no row.
agent_home_field() {
  local adapter="$1" col="$2"
  agent_home_table | awk -v a="$adapter" -v c="$col" '$1 == a { print $c; found = 1 } END { exit !found }' || true
  return 0
}

# agent_home_var <adapter> prints the env var that relocates the adapter's home,
# or nothing when it has no row.
agent_home_var() {
  local adapter="${1:-}"
  agent_home_field "$adapter" 2
  return 0
}

# agent_home_policy <adapter> prints "default" or "optin", or nothing.
agent_home_policy() {
  local adapter="${1:-}"
  agent_home_field "$adapter" 3
  return 0
}

# agent_home_isolate <adapter> <staging-default-dir> resolves, validates,
# exports and creates the adapter's home, and reports on stdout what it did.
#
# It ALWAYS prints exactly one `agent-home:` line, including for an adapter with
# no row and for an opt-in one the caller left unset. That is deliberate: an
# isolation step whose "did nothing" and "worked" cases are both silent is
# indistinguishable from one that was never reached, which is the defect the
# tmux measurement in this file's header is an instance of.
#
# A RELATIVE value is a hard failure, never a fallback. The daemon resolves
# these through agentpaths.FromEnv, which logs and IGNORES a non-absolute value
# and falls back to $HOME — so accepting one here would point the daemon at the
# real home while the driver used the relative one, which is the same
# daemon/CLI disagreement in a different disguise (#1388 recorded it for codex).
agent_home_isolate() {
  local adapter="${1:-}" staging_default="${2:-}"
  local var policy current

  if [[ -z "$adapter" ]]; then
    echo "agent-home: usage: agent_home_isolate <adapter> <staging-default-dir>" >&2
    return 1
  fi

  var="$(agent_home_var "$adapter")"
  policy="$(agent_home_policy "$adapter")"

  if [[ -z "$var" ]]; then
    echo "agent-home: $adapter — no home override is wired (lib/agent-home.sh); the daemon and the CLI both use the real \$HOME"
    return 0
  fi

  # Indirect expansion rather than `${!var}` gymnastics at every use site; bash
  # 3.2 supports it and it keeps the value read in one place.
  current="${!var:-}"

  if [[ -z "$current" && "$policy" == "default" ]]; then
    if [[ -z "$staging_default" ]]; then
      echo "agent-home: $adapter declares policy 'default' but no staging directory was passed" >&2
      return 1
    fi
    current="$staging_default"
  fi

  if [[ -z "$current" ]]; then
    echo "agent-home: $adapter — NOT isolated; export $var=<absolute path> to keep this run off the real home"
    return 0
  fi

  if [[ "$current" != /* ]]; then
    echo "agent-home: $var must be absolute (got '$current')" >&2
    echo "            the daemon resolves it through agentpaths.FromEnv, which IGNORES a" >&2
    echo "            relative value and falls back to \$HOME — so the daemon and the agent" >&2
    echo "            CLI would watch and write two different homes, and the fixture would" >&2
    echo "            come back empty with nothing saying why" >&2
    return 1
  fi

  if ! mkdir -p "$current"; then
    echo "agent-home: cannot create $var=$current" >&2
    return 1
  fi

  export "$var=$current"
  echo "agent-home: $adapter — $var=$current (daemon + driver share this home)"
  return 0
}
