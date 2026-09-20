## 1. Bounded behavior

The parser drops the field.

## 5. Affected ports, adapters, clients and stored formats

- `core/adapters/inbound/agents/pi/parser.go:86` — `handleNonMessageEvent`.
- `core/adapters/inbound/agents/pi/parser_test.go` — the `model_change` cases.
- The `tailer.ParsedEvent` fields that carry the evidence.

## 6. Input fixtures and the expected visible result
