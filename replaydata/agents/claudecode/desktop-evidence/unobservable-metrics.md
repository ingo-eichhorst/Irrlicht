# Metrics no execution profile can record

Two claudecode scenarios assert facts the recording format cannot carry. This
is a property of the **fixture format**, not of Claude Desktop: `cli-local`
cannot record them either, and neither could a new front end. They are marked
`unobservable` rather than `not-runnable`, because the driver could perform
the recipe perfectly and the recording would still not contain the assertion.

Verified 2026-09-06 against `core/domain/lifecycle/event.go`.

## Reproduce

```sh
# The complete recorded-event field set:
awk '/^type Event struct/,/^}/' core/domain/lifecycle/event.go

# Every rate-limit / quota / provider / route field in the event model:
grep -rniE 'ratelimit|rate_limit|quota|provider|route' core/domain/lifecycle/event.go
```

The second command returns exactly one line, a comment on line 237 describing
provider *text*. There is no such field.

## 5-5 provider-failover-midturn

The scenario asserts that a mid-turn provider failover is visible.

Provider routing is transport-layer. It never changes `message.model` and
never writes any transcript field, so it leaves no trace in `events.jsonl` or
in the transcript. `lifecycle.Event` has no `provider` or `route` field to
carry one.

Claude Desktop reads the identical JSONL, so it supplies no new signal. This
would change only if upstream began emitting a `provider` or `route` field.

## 5-7 quota-burndown

The scenario asserts that quota burn-down is visible over a session.

`lifecycle.Event` has no rate-limit field, and the fixture format cannot carry
the statusline snapshot that holds the counters — for any front end. Making
this observable needs one of:

- a recordable lifecycle event carrying rate-limit fields, or
- a live-metrics probe spec, which the fixture format does not have.

Note the distinction from cost and tokens: those ARE derivable, because
`EstimatedCostUSD` (`core/domain/session/metrics.go`) is computed from
transcript token counts against a pricing table. Quota is a subscription
counter taken from the agent, never reconstructed by us, and nothing in the
recording carries it.
