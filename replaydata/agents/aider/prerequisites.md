# Prerequisites — aider

Maintainer-authored checklist for `agent-onboard record` against aider.

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-aider.ok
```

## Items

- [ ] `aider` CLI installed (`aider --version` works). pipx install recommended.
- [ ] At least one model provider configured (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.)
      — aider needs paid API access; subscription-only login isn't supported.
- [ ] git available — aider commits per turn.
- [ ] tmux + lsof — same as other agents.

## Notes

- aider has no formal `session_id` concept; irrlicht discovers sessions
  lazily from `.aider.chat.history.md` writes. The
  `session-resume` / `session-reset` scenarios may report `partial` /
  `unknown` for this reason.
- `architect-editor-pair` requires `--architect` mode and two model
  providers (a strong reasoner + a cheap editor).
- `provider-failover-midturn` requires deliberately exhausting the
  primary provider's quota — practical only with a low-quota dev key or
  intentional 429 injection.
