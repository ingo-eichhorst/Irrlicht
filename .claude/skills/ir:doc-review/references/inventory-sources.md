# Inventory sources — the code-derived truth for axes C and V

These recipes derive the project's real surfaces **from code, each run**. Axes C (completeness)
and V (validity) compare documentation against the output of these recipes — never against a
remembered list. All commands run from the repo root (`git rev-parse --show-toplevel`).

If a recipe stops matching because code moved, **fix the recipe here** and note the drift in the
run summary. Do not hardcode the resulting set.

The recipes below were verified against the tree at the time of writing; the counts in
parentheses are illustrative, not assertions to trust blindly — recompute every run.

---

## 1. Agent adapters (currently 7)

Authoritative: `core/adapters/inbound/agents/all.go`, function `All()`. The slice order is the
canonical order.

```bash
sed -n '/^func All()/,/^}/p' core/adapters/inbound/agents/all.go \
  | grep -oE '[a-z]+\.Agent\(\)' | sed 's/\.Agent()//' | sort
# → aider claudecode codex geminicli kirocli opencode pi
```

Per-adapter `Capabilities`/`Permissions` follow the `agent.Agent` struct idiom in
`core/domain/agent/declaration.go`; each adapter's `Agent()` lives at
`core/adapters/inbound/agents/<name>/agent.go`. Orchestrators are separate:
`core/adapters/inbound/orchestrators/*` (e.g. `gastown`).

**Documented mention** = the adapter's display name appears in a "supported agents" context
(README feature list, `site/index.html` agent grid, `site/docs/adapters.html`).

## 2. CLI binaries and flags

Authoritative: directories under `core/cmd/*` and `tools/*/cmd/*`. Flags use the Go stdlib
`flag` package (`flag.StringVar` / `flag.BoolVar` / `FlagSet`). `irrlichd` is the exception —
it parses a few flags manually via a `hasFlag()` helper.

```bash
# binaries
find core/cmd tools/*/cmd -maxdepth 1 -mindepth 1 -type d 2>/dev/null | sort
# flags defined via the flag package
grep -rhoE 'flag\.[A-Za-z]+Var\([^,]+,\s*"[^"]+"' core tools --include='*.go' \
  | grep -oE '"[^"]+"' | sort -u
# irrlichd's manual flags
grep -oE 'hasFlag\("[^"]+"' core/cmd/irrlichd/main.go | sed 's/hasFlag("//' | sort -u
```

User-facing binaries: `irrlichd`, `irrlicht-ls`, `irrlicht-focus`, `irrlichtrelay`. Treat the
rest (`tools/onboarding-factory/cmd/*` such as `of`, `viewer`, `replay`) as internal dev tools —
a missing doc mention for those is at most Minor (contributor-facing), per the rubric.

**Documented mention** = the binary and its user-facing flags appear in `site/docs/cli-tools.html`
or the README.

## 3. Config / environment variables

Authoritative: `os.Getenv` call sites. Central defaults live in `core/domain/config/config.go`.

```bash
grep -rhoE 'os\.Getenv\("[A-Z_]+"\)' core --include='*.go' \
  | grep -oE '"[A-Z_]+"' | tr -d '"' | sort -u
```

The **config inventory** that completeness checks against is the user-facing subset only:

- **Include:** every var matching `^IRRLICHT_`, plus the documented externals `NO_COLOR`,
  `GT_BIN`, `GT_ROOT` (Gas Town orchestrator config).
- **Exclude (not user config):** `HOME`, `XDG_CONFIG_HOME` (standard OS env); and all
  test/build/helper vars — `GO_WANT_*`, `OBSERVER_HELPER_*`, `GASTOWN_FIXTURES_DIR`, and
  anything only read under `_test.go`.

```bash
grep -rhoE 'os\.Getenv\("(IRRLICHT_[A-Z_]+|NO_COLOR|GT_BIN|GT_ROOT)"\)' core --include='*.go' \
  | grep -v '_test.go' | grep -oE '"[A-Z_]+"' | tr -d '"' | sort -u
```

**Documented mention** = the var appears in `site/docs/configuration.html` (or README for the
common ones like `IRRLICHT_BIND_ADDR`, `NO_COLOR`).

## 4. Daemon HTTP routes

Authoritative: `core/cmd/irrlichd/main.go`, registered on a stdlib `http.ServeMux`.

```bash
grep -oE 'mux\.HandleFunc\("[A-Z]+ [^"]+"' core/cmd/irrlichd/main.go \
  | sed 's/mux.HandleFunc("//' | sort
```

Public API routes are the `/api/v1/*` set; `/debug/pprof/*` and `/state` are internal
(localhost / debug). The relay server exposes its own routes from `core/cmd/irrlichtrelay/`.

**Documented mention** = the route appears in `site/docs/api-reference.html`.

## 5. Session states (exactly 3) and lifecycle events

Authoritative states: `core/domain/session/session.go`.

```bash
grep -oE 'State(Working|Waiting|Ready)\s*=\s*"[a-z]+"' core/domain/session/session.go
# → working / waiting / ready  (the count "three" is checkable here)
```

Authoritative event kinds: `core/domain/lifecycle/event.go`.

```bash
grep -oE 'Kind[A-Za-z]+\s+Kind\s+=\s+"[a-z_]+"' core/domain/lifecycle/event.go
```

**Documented mention** = states/events described in `events.md`, `site/docs/state-machine.html`,
`site/docs/session-detection.html`. The literal claim "three states" must equal the count above
(axis V, V2).

## 6. Relay wire protocol

Authoritative: `core/adapters/outbound/relay/envelope.go`. Documented in `docs/relay-protocol.md`.

```bash
grep -nE 'const ProtocolVersion' core/adapters/outbound/relay/envelope.go      # the wire version
grep -oE 'Msg[A-Za-z]+\s+=\s+"[a-z_]+"' core/adapters/outbound/relay/envelope.go  # frame types
```

**Note (read carefully — see the rubric's false-positive traps):** the doc heading
"Relay wire protocol (v0)" names the *feature milestone*, while `hello.protocol_version`
(value `1`) is the *wire-format version*. The doc itself reconciles these. Do not flag the "v0"
heading as contradicting `ProtocolVersion = 1` — flag only a genuine, unreconciled mismatch
(e.g. a frame type listed in the doc that no longer exists in `envelope.go`, or vice versa).

## 7. Version string and user-facing counts

Authoritative version: `version.json` (base) and `tools/version.sh` (full dev string).

```bash
python3 -c "import json;print(json.load(open('version.json'))['version'])"  # base, e.g. 0.5.1
bash tools/version.sh --base                                                # same, via the script
```

Derived user-facing numbers to validate doc claims against (axis V, V2/V5):
- **supported-agents count** = the adapter count from recipe 1.
- **default daemon port** `7837`, **relay port** `7839` — `core/cmd/irrlichd/main.go`,
  `core/cmd/irrlichtrelay/main.go`.
- **CHANGELOG head version** should match `version.json` at release time.

---

## Mapping inventories → axes

- **C1 (coverage):** every item from recipes 1–6 has ≥1 documented mention. A user-facing item
  with zero mention is the finding (name the item + its authoritative source).
- **C2 (exhaustive lists):** every documented "supported X" list equals the inventory set
  (typically agents, recipe 1).
- **V1 (references resolve):** every path/symbol/command/flag a doc names exists per recipes
  1–4 (or `--help`).
- **V2 (counts match):** numeric claims equal the recipe-derived count (recipes 1, 5, 7).
- **V5 (currency):** version/port/"latest release" claims equal recipe 7.
