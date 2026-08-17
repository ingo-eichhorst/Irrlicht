# Irrlicht Elfdans — device test plan

**Status:** not yet run. Everything it grades is built (slices 1–9), so this is the gate on
calling the feature done — and it should run again before any release that touches the push path.
**Why it exists:** every defect found in Elfdans so far was found by *reading* code. Two of them —
a VAPID JWT with no `sub` claim, which Apple rejects, and `pushManager.subscribe()` called before
the service worker activates, which the Push API rejects — fail **only on contact with a real
push service or a real device**, and passed every mock in the suite. There is no reason to think
they were the last two of their kind. Nothing in this feature has been observed working.

The plan is ordered so that **each phase adds exactly one new variable**. If a phase fails, the
fault is in what that phase introduced, not in the four before it.

---

## Automated: `tools/elfdans-rig.sh`

Setup friction is what makes this plan expensive to repeat, so most of it is scripted:

```sh
tools/elfdans-rig.sh up [--serve]   # build + start an isolated relay, auth on, own state dir
tools/elfdans-rig.sh check          # every assertion below that needs no phone
tools/elfdans-rig.sh down [--wipe]
```

State lives in `.build/elfdans-rig` — never your real `~/.local/share/irrlicht` — so nothing here
can touch a production relay's tokens or a paired phone. `up` refuses rather than starting a
second relay on a busy port, because `7839` is the production default and the thing already
there is probably yours.

`check` currently covers, against a live relay: the anonymous-mode refusal (403 naming the fix,
and **no VAPID key minted**), `push/info`, `0600` file modes, mint → redeem → subscribe, a spent
code answering the uniform 401, the health endpoint not echoing the endpoint path (RFC 8030
§8.3), a malformed subscription refused at the door, VAPID identity and subscriptions surviving a
restart, and `token revoke` pruning the delivery address. That is Phase 0's relay half plus most
of Phase 6.

**It cannot cover what needs a device:** the iOS install and storage partition, permission and
activation ordering, whether a real push service accepts our JWT, and whether a banner actually
appears on a locked phone. Those are Phases 3, 4 and the visual half of 5, and they are the whole
reason this document exists.

## What you need

- The Mac, with a dev relay and daemon (`tools/build-dev.sh`; `/ir:test-mac separate` gives an
  isolated daemon on `:7838` so your production setup is untouched).
- **A real HTTPS origin.** iOS refuses to install a web app or grant notification permission on
  plain HTTP, and `localhost` is not reachable from the phone. `tailscale serve --bg 7839` gives
  you `https://<mac>.<tailnet>.ts.net` with a valid certificate and no port-forwarding.
- An iPhone on the same tailnet (Tailscale app), or `tailscale funnel` if you'd rather not.
- **Safari Web Inspector** — this is the debugging tool for the whole plan. On the phone:
  Settings → Safari → Advanced → Web Inspector. On the Mac: Safari → Develop → *\<your phone\>* →
  the Elfdans app. Without it, a phone-side failure is a blank screen with no diagnosis.

**The "send a test notification" button exists** (arc42 §8.3, slice 9): once a phone is paired, the
Elfdans health panel offers it, and it answers on the spot — `Sent via web.push.apple.com…`, or the
push service's own refusal. Use it wherever a phase below needs "a push, any push". It is not a
substitute for the phases that drive real transitions: it bypasses the policy engine, so it proves
the *delivery* half (VAPID, encryption, subscription, the phone's notification settings) and nothing
about whether an agent going `waiting` would be noticed. Phases 4 and 5 are still the only evidence
for that half.

---

## Phase 0 — the desktop browser first

**This is the highest-value step in the plan and the easiest to skip.** A desktop browser on the
Mac speaks the same Web Push protocol through the same push services, with none of iOS's install
and storage-partition complexity — and with full DevTools. Pair Chrome (or Safari) on the Mac
against the relay *before* you touch the phone.

Everything in the crypto and delivery path is exercised here: VAPID signing and its acceptance by
the push service, RFC 8291 encryption, the payload shape, the service worker's `push` handler,
tag-based collapse. If push works on the desktop and fails on the phone, the fault is in the iOS
half — install, activation, permission gesture — which is a quarter of the surface.

| Step | Expect | If it fails |
|---|---|---|
| Relay with `--auth tokens-file`, `GET /api/v1/push/info` | `{"enabled":true,"vapid_public_key":"…"}` | No key: `vapid-keys.json` unwritable. `enabled:false`: auth is off — that is the §8.1 guard working |
| Same call with `--auth off` | `enabled:false` + reason; `POST /push/pairings` → **403** naming the fix | The guard regressed — this is a one-line check that the whole anonymous-mode refusal still holds |
| Pair the desktop browser through the UI | Subscription lands in `push-subscriptions.json` | DevTools console. Suspect the activation ordering |
| **Press "Send a test notification"** | Banner on the desktop, and the panel reads `Sent via …` | The panel names the failure itself — a 4xx here is the VAPID `sub` class of bug, and it now arrives in front of you rather than in the relay log. This is the cheapest possible confirmation that signing and encryption are accepted by a real push service |
| Trigger one `waiting` transition | Banner on the desktop | Only now is the relay log the diagnosis: the button already proved delivery, so a silent `waiting` means the transition was never observed or policy suppressed it |

Only move on once a desktop banner has actually appeared.

---

## Phase 1 — the relay and the origin

| Step | Expect |
|---|---|
| `tailscale serve --bg 7839`, open the URL from the phone's browser | The dashboard loads over HTTPS with a valid cert |
| `vapid-keys.json`, `tokens.json` on disk | Mode `0600` |
| Restart the relay | The VAPID public key from `push/info` is **unchanged** — subscriptions are bound to it |

## Phase 2 — the daemon reaches the relay

Point the dev daemon at the relay (`IRRLICHT_RELAY_URL=…` plus its token) and confirm real
sessions appear in the **relay-served** dashboard. This proves the hub path end to end before push
is in the picture — if sessions never arrive, no notification ever could, and that is a relay-link
problem, not a Elfdans one.

## Phase 3 — install and pair on the phone

The highest-risk phase, and the one the mocks cover worst.

1. Open the origin in Safari on the phone. **The Elfdans section must appear** — that is feature
   detection working against a real origin (§5.2).
2. Share → **Add to Home Screen**. Open it **from the home screen**, not from Safari.
3. Mint a code in the dashboard on the Mac. Type it into the installed app.
4. Watch for, in order: the permission prompt appearing at all (if it does not, transient user
   activation expired — the known ordering bug); `subscribe()` resolving (if it rejects, the
   worker had not activated); `POST /push/subscriptions` → 204.
5. **Press "Send a test notification"** and watch the phone. This is the whole of Phase 3's payoff
   in one tap: it proves the pairing produced a working delivery address on *this* device, without
   touching an agent. If the panel says it was sent and no banner appears, the fault is on the
   phone (notification permission, Focus mode) rather than in the relay — the two used to be
   indistinguishable from here.

**Verify the ADR-3 claim while you are here**: pairing must complete *inside the installed app*.
Pair from the Safari tab instead and confirm the installed app does **not** inherit it — that is
the storage partition the whole one-time-code design exists for. If it turns out to be inherited,
ADR-3 is over-engineered and should be simplified.

On failure: Web Inspector console first, relay log second, `push-subscriptions.json` third.

## Phase 4 — the first real push

Lock the phone. Drive one session to `waiting`. The test notification does **not** cover this
phase — it is a direct dispatch, so what is new here is everything between an agent's transcript
write and the dispatcher: the daemon's forward, the relay's observer, and §8.4's policy. Run it even
if the button worked, and *especially* if it worked, because that narrows a failure here to exactly
that half.

- **Expect:** a lock-screen banner within about 3 seconds (arc42 Q1).
- Tap it. (Note: R6 says it should open on that session's last-known state. If the ledger read
  path has not landed, it will just open the app — record which behavior you see.)
- **If nothing arrives**, press the test button first: it separates the two halves in one tap. A
  test notification that *does* arrive means delivery is fine and the fault is upstream of the
  dispatcher — the daemon's link, the observer, or policy. One that does not narrows it to signing,
  encryption or the subscription, and the panel names which. The relay log carries the same reason
  with the endpoint path redacted, and `GET /api/v1/push/subscriptions` gives that phone's last
  delivery outcome.

## Phase 5 — the policy actually behaves (§8.4)

These are the promises that make the feature bearable rather than annoying; every one is
observable from the couch with a locked phone.

| Behavior | How to drive it | Expect |
|---|---|---|
| Ready hold-down | Let a turn finish | Banner after ~7s, not instantly |
| Hold-down cancel | Finish a turn, then send a new prompt within 7s | **No banner at all** |
| Collapse | Two transitions on one session | The banner is *replaced*, never two stacked |
| Burst | Four sessions into `waiting` inside 20s | One "N agents need attention", not four |
| Subagents | A session that spawns subagents | Only the parent notifies |
| Watchdog | Kill the daemon, wait | "disconnected" banner after ~60s |
| Watchdog cancel | Kill and restart the daemon within 60s | **Nothing** |
| Reconnect | Restart after the disconnect banner | The banner is replaced *silently* — no second buzz |

## Phase 6 — failure and recovery

| Scenario | Expect |
|---|---|
| `irrlichtrelay token revoke <device-id>` | Pushes stop; the entry is pruned from the registry. The test button is the fast check: it answers 401 and the panel reports the phone as no longer paired, rather than leaving you waiting for a push that will never come |
| Un-pair from the phone | `DELETE` succeeds, entry gone, no further pushes |
| Restart the relay | Phones keep working, no re-pairing |
| Delete `push-subscriptions.json`, reopen the app | It re-registers itself (§8.3 self-heal), and the test button confirms the re-registered address actually delivers — a repaired *record* and a working *address* are not the same claim |
| Restore all four files onto a *fresh* relay at the same hostname | Every phone still works — this is the Oracle Cloud reclamation story, and the only way to know it holds is to do it once |

## Phase 7 — the additivity contract (Q7)

Easy to forget and the whole point of §5.2. Run the daemon-served dashboard on `127.0.0.1` with
**no relay configured** and confirm:

- No Elfdans section anywhere in Settings.
- **No service worker installed** — DevTools → Application → Service Workers is empty.
- Zero connection attempts to any relay.

A failure here is worse than a broken notification: it means the feature changed something for
users who never asked for it.

---

## What this plan does not cover

Stated so nobody mistakes a pass for more than it is:

- **Android/FCM** — no device in the loop. The path is the same standard, but it is untested.
- **Long-term subscription expiry.** iOS drops push subscriptions after extended inactivity; the
  self-heal is designed for it, but observing it takes weeks, not an afternoon.
- **Oracle Cloud specifically** — Phase 1-7 on a tailnet do not exercise OCI's firewall gates or
  its idle-reclamation behavior.
- **iOS version spread.** One phone, one OS version.

## Capture, for each phase you run

A failure is only useful if it is diagnosable later: the relay log lines, the
`GET /api/v1/push/subscriptions` health JSON, the Web Inspector console, and which of the four
state files existed with what contents. Record passes too — an "it worked" with no evidence is
the same claim this document was written to stop making.
