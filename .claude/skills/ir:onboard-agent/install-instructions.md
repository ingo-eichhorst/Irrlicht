# Install / Auth Instructions

Loaded by `skill.md` when `classify-failure.sh` returns a code that
warrants user action. The skill never runs install commands itself —
this doc is what the skill shows the user.

## claudecode

**Install:**
```bash
npm install -g @anthropic-ai/claude-code
# or:
brew install claude-code
```

**Auth:** subscription login.
```bash
claude /login
```
Follow the browser flow.

**Verify:**
```bash
claude --version
echo "hello" | claude --print
```

**Common failure markers** (in `driver.log`):
- `command not found: claude` — install needed
- `Please log in` / `authentication required` / `401` — auth needed

## codex

**Install:**
```bash
npm install -g @openai/codex
# or follow https://github.com/openai/codex
```

**Auth:** API key in `~/.codex/config.toml`:
```toml
[auth]
api_key = "sk-..."
```

**Verify:**
```bash
codex --version
codex exec --json "hello"
```

**Common failure markers:**
- `command not found: codex`
- `OPENAI_API_KEY not set` / `401 Unauthorized`

## pi

**Install:**
```bash
brew install pi-agent
# or pip install pi-agent (varies by distribution)
```

**Auth:** provider key.
```bash
pi --api-key sk-... --provider anthropic
# or set env var PI_API_KEY
```

**Verify:**
```bash
pi --version
pi --print -p "hello"
```

**Common failure markers:**
- `command not found: pi`
- `no API key configured`

## Failure-class to action map

| Code from classify-failure.sh | Show user |
|---|---|
| `cli_not_found` | Install section above for the relevant adapter |
| `cli_too_old` | Update command (`npm install -g <pkg>@latest` or equivalent) plus `min_versions` requirement from `scenarios.json` |
| `auth_failed` | Auth section above |
| `daemon_dirty` | "Stop the production irrlichd: `launchctl unload ~/Library/LaunchAgents/io.irrlicht.plist` or quit the menu-bar app." |
| `working_tree_dirty` | "Commit or stash your changes under `replaydata/agents/`, then retry." |
| `transcript_missing` | Show `<staging>/daemon.log` and `<staging>/driver.log`; suggest checking that the scenario's prompt keeps the session alive long enough |
| `timeout` | Show `<staging>/driver.log`; suggest increasing `timeout_seconds` in the scenario |
| `unknown` | Just point at `<staging>/driver.log` and `<staging>/run-manifest.json` |
