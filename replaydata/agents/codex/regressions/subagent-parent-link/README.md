# Codex subagent parent-link fixture

Redacted Codex 0.144.4 rollout headers from issue #1050: one parent and three
sibling child threads. Each child keeps its own `payload.id` and carries the
same authoritative `source.subagent.thread_spawn.parent_thread_id`. The shared
`payload.session_id` is deliberately present only to prove it is not identity.
