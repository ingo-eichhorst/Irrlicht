import AppKit
import Foundation

// MARK: - Keepalive tick + system-wake recovery (#1953)
//
// Before this file neither link had any ongoing liveness check. The single
// `sendPing` in `connect()` fires once per connect attempt and feeds
// `recordConfirmedLocalConnect()`; nothing re-arms it, so it is a
// connect-confirmation probe, not a keepalive. That left one failure mode with
// no recovery at all: a Mac waking from standby can leave a WebSocket
// half-open — the peer is gone, but no FIN and no error arrive — so
// `receive()` waits forever and the backoff reconnect behind it never runs.
//
// The local link survived that in practice for a reason unrelated to its
// socket: `SessionManager+Hydration.swift`'s 30s `projectCostsTimer` re-fetches
// `/api/v1/sessions` over REST, so local rows self-heal behind a dead
// WebSocket. `relaySessionMap` has no such fallback — it is fed only by the
// relay WebSocket — so the relay was the visible casualty (#1953).
//
// Two mechanisms, both reusing the reconnect machinery that already exists
// rather than adding a second one:
//
//   1. A repeating tick pings every live link and, when one has been silent
//      past `livenessStaleDeadline`, cancels its socket task. That makes the
//      parked `receive()` throw, which is the one thing the existing backoff
//      reconnect in `connect()` / `relayConnect()` was waiting for.
//   2. `NSWorkspace.didWakeNotification` does the same immediately, so
//      recovery after a wake does not have to wait out the deadline.

extension SessionManager {
    /// Starts the repeating liveness tick. Idempotent.
    ///
    /// Deliberately NOT gated on `isRunningUnitTests`, unlike
    /// `startProjectCostsPolling()`. That gate exists because a cost poll is an
    /// unconditional daemon round-trip (#832); this tick does I/O — a
    /// `sendPing` — only for a link that already has a socket task, and under
    /// `swift test` `webSocketTask` / `relayWebSocketTask` are nil unless a test
    /// assigns one. So with nothing connected the tick is a no-op (pinned by
    /// `SessionManagerLivenessTests.testTickIsANoOpWithNoLiveLinks`). Verified
    /// beyond that one test by running `swift test --skip LauncherTestHarness
    /// --skip LauncherHarnessTests` green with the timer armed in every manager
    /// the suite constructs.
    func startConnectionLivenessPolling() {
        livenessTimer?.invalidate()
        let timer = Timer.scheduledTimer(withTimeInterval: livenessPingInterval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.checkConnectionLiveness()
            }
        }
        // A zero-tolerance timer cannot be coalesced with any other wakeup, and
        // this is a menu-bar app that runs all day: at 20s that is ~4300 forced
        // wakeups per day for a check whose deadline is 60s, so a couple of
        // seconds of drift costs nothing. Measured during review: the default
        // `tolerance` on a `Timer.scheduledTimer` is 0.0, and the app's three
        // other repeating timers all still take it — named here rather than
        // changed, since they are outside this fix.
        timer.tolerance = livenessPingInterval * 0.1
        livenessTimer = timer
    }

    /// Binds `handleSystemDidWake()` to `NSWorkspace.didWakeNotification`.
    ///
    /// The notification name is fixed, so reading this signature answers "what
    /// does this app observe?". Only the CENTER is injectable, and only because
    /// posting a wake on the process-global
    /// `NSWorkspace.shared.notificationCenter` would run the handler on every
    /// other live `SessionManager` in the test process — each registers here in
    /// `init`, and only `deinit` unregisters. Tests therefore post the genuine
    /// notification name on a private center
    /// (`…testAWakeNotificationRecoversBothLinks`), and the center default is
    /// pinned separately (`…testInitRegistersASystemWakeObserver`).
    ///
    /// Registering twice replaces the first observer instead of stacking a
    /// second, so a re-registration cannot double-fire the handler.
    func observeSystemWake(_ center: NotificationCenter = NSWorkspace.shared.notificationCenter) {
        if let systemWakeObserver {
            systemWakeCenter?.removeObserver(systemWakeObserver)
        }
        systemWakeCenter = center
        systemWakeObserver = center.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.handleSystemDidWake() }
        }
    }

    /// Which link a liveness tick is talking about.
    ///
    /// An enum rather than a pair of key paths because the pong handler is a
    /// `@Sendable` closure: a `ReferenceWritableKeyPath` captured there is a
    /// concurrency warning (seen, then removed), while this is a trivially
    /// `Sendable` value.
    enum LivenessLink: Sendable, Equatable {
        case local
        case relay

        var label: String {
            switch self {
            case .local: return "Local daemon"
            case .relay: return "Relay"
            }
        }
    }

    /// Why a socket was torn down, and which link it belonged to.
    enum LivenessTeardown: Sendable, Equatable {
        /// The link stayed silent past `livenessStaleDeadline`.
        case stale(LivenessLink)
        /// The machine woke and every socket open across the sleep is suspect.
        case wake(LivenessLink)
    }

    /// One liveness tick: for each live link, either reap it as stale or ping
    /// it. `now` is a parameter so a test can place the deadline where it wants
    /// it without waiting on a clock.
    func checkConnectionLiveness(now: Date = Date()) {
        tickLink(.local, now: now)
        tickLink(.relay, now: now)
    }

    private func socketTask(for link: LivenessLink) -> URLSessionWebSocketTask? {
        switch link {
        case .local: return webSocketTask
        case .relay: return relayWebSocketTask
        }
    }

    private func lastFrameAt(for link: LivenessLink) -> Date? {
        switch link {
        case .local: return lastLocalFrameAt
        case .relay: return lastRelayFrameAt
        }
    }

    private func setLastFrameAt(_ date: Date?, for link: LivenessLink) {
        switch link {
        case .local: lastLocalFrameAt = date
        case .relay: lastRelayFrameAt = date
        }
    }

    /// A liveness ping came back. That is proof the socket is alive, so it is
    /// both a fresh stamp and — for a link that has not confirmed yet — the
    /// confirmation itself.
    ///
    /// The confirmation half is not decoration. `connect()` has carried its own
    /// confirmation ping since #843 precisely because "a daemon with no tracked
    /// sessions can go the entire cycle without pushing a single application
    /// message"; the relay path inherits the same problem the moment
    /// `.connected` stops being set eagerly, because `relayServerURL` is
    /// allowed to point at a bare `irrlichd` (see `relayConnect()`'s hello
    /// comment and `handleRelayMessage`'s raw-frame `default:` branch) and that
    /// daemon writes only one snapshot per known session — nothing at all when
    /// it has none. Without this the link would sit `.connecting` forever on a
    /// perfectly healthy socket.
    ///
    /// Both `recordConfirmed…Connect()` calls are idempotent, so this is safe
    /// on every pong of an already-connected link.
    func recordLivenessPong(for link: LivenessLink) {
        setLastFrameAt(Date(), for: link)
        switch link {
        case .local: recordConfirmedLocalConnect()
        case .relay: recordConfirmedRelayConnect()
        }
    }

    /// Tears one socket down and records that it happened.
    ///
    /// Every liveness teardown goes through here so `livenessTeardowns` records
    /// which link and why — see that property for what the task itself cannot
    /// tell a test.
    ///
    /// Plain `cancel()`, not `cancel(with:reason:)`: this is an abnormal
    /// teardown, and `relayConnect()` reads `task.closeCode` afterwards to tell
    /// a token rejection (4401) from an ordinary drop, so a teardown must not
    /// put a code on the wire. **Stated, not asserted** — the measurement, and
    /// why no unit test in this suite can check it, is recorded once in
    /// `SessionManagerLivenessTests`' "NOT ASSERTED" note.
    private func dropSocket(_ task: URLSessionWebSocketTask, _ reason: LivenessTeardown) {
        livenessTeardowns.append(reason)
        if livenessTeardowns.count > livenessTeardownHistoryLimit {
            livenessTeardowns.removeFirst(livenessTeardowns.count - livenessTeardownHistoryLimit)
        }
        task.cancel()
    }

    /// The system woke from standby. Every socket that was open across the
    /// sleep is suspect — it may be half-open with no error to report — so tear
    /// each one down at once and let the loop parked on it reconnect, rather
    /// than waiting out `livenessStaleDeadline`.
    ///
    /// This is the test seam: tests call it directly and never post a real
    /// `NSWorkspace` notification (except the one that checks the binding
    /// itself).
    ///
    /// The backoff resets come first so that the reconnect each `cancel()`
    /// releases dials in ~1s rather than at whatever delay the link had backed
    /// off to. Nothing in this method suspends — it is `@MainActor` (via the
    /// class) and contains no `await` — so every write below lands before any
    /// continuation woken by the cancels can run.
    ///
    /// That same ordering means the reset cannot also protect the failure
    /// STREAK: the cancel is what ends the parked cycle, and the cycle counts
    /// itself a failure on the way out if the link never confirmed. Both tails
    /// therefore gate that count on `…ConnectionState != .connected`
    /// (`SessionManager+WebSocket.swift`, `SessionManager+Relay.swift`), so a
    /// wake on a CONFIRMED link — the case this method exists for — adds
    /// nothing to the streak. A wake on a link that never confirmed does count,
    /// which is correct: it did fail.
    ///
    /// The backoff resets and stamp clears are unconditional; only the CANCEL
    /// is per-socket, because only a socket can be wedged. A link that is
    /// disabled in Sources, or parked between reconnect attempts, therefore
    /// still gets its backoff cleared — harmless (it re-dials at 1s instead of
    /// its backed-off delay, which is what a wake should do anyway) and worth
    /// stating, since `resetRelayConnectBackoff()` also clears
    /// `relayConnectionStalled`, a `DaemonHealth.faults` input. A link with no
    /// socket is otherwise left to its own bounded reconnect loop, whose worst
    /// case is `maxReconnectDelay` (30s, `SessionManager.swift`).
    func handleSystemDidWake() {
        print("🔌 System woke from standby — revalidating both links")
        resetLocalConnectBackoff()
        resetRelayConnectBackoff()
        lastLocalFrameAt = nil
        lastRelayFrameAt = nil
        if let task = webSocketTask { dropSocket(task, .wake(.local)) }
        if let task = relayWebSocketTask { dropSocket(task, .wake(.relay)) }
    }

    /// Reap-or-ping for one link.
    ///
    /// Reaping is a bare `cancel()` on the socket task and nothing else: the
    /// `receive()` parked on it throws, and `connect()` / `relayConnect()` run
    /// their own existing backoff reconnect from there. Clearing the stamp
    /// before the cancel is what stops the next tick from reaping the
    /// replacement socket before its first frame — `ConnectionLiveness.isStale`
    /// reads a nil stamp as "not stale".
    private func tickLink(_ link: LivenessLink, now: Date) {
        guard let task = socketTask(for: link) else { return }

        // A socket with no clock gets one started here rather than being
        // granted immortality. A link that completes its handshake and then
        // goes silent never delivers a first frame and never gets a pong, so
        // nothing else would ever stamp it — and `isStale` reads a nil stamp as
        // "not stale", which for a live socket would mean "never reap". That is
        // #1953's own failure mode one connect earlier, and a post-wake
        // reconnect is exactly how it is reached, so leaving it out would have
        // let the bug survive its own fix.
        //
        // Started here rather than at each `resume()` so a third connect path
        // cannot forget, at a cost of at most one `livenessPingInterval` before
        // the deadline begins. Red-first evidence:
        // `SessionManagerLivenessTests.testTickStartsAClockForASocketThatHasNoneAndLaterReapsIt`.
        if lastFrameAt(for: link) == nil {
            setLastFrameAt(now, for: link)
        } else if ConnectionLiveness.isStale(lastFrameAt: lastFrameAt(for: link),
                                             now: now,
                                             deadline: livenessStaleDeadline) {
            print("🔌 \(link.label) silent for over \(Int(livenessStaleDeadline))s — dropping the socket so the reconnect loop runs")
            setLastFrameAt(nil, for: link)
            dropSocket(task, .stale(link))
            return
        }

        // Every link that survives the tick gets pinged — one call site, so
        // "a live link is always probed" cannot drift between branches.
        probe(task, link)
    }

    /// Pings one link. A pong is the only liveness evidence an idle link ever
    /// produces: the relay pings its clients every 30s
    /// (`core/cmd/irrlichtrelay/hub.go:22`, read at `a680d5ea`) but URLSession
    /// answers those below this API and reports nothing, and a daemon with no
    /// tracked sessions can push no application frame for minutes —
    /// `connect()`'s own comment says as much. Without this the deadline would
    /// reap perfectly healthy idle links.
    ///
    /// Not `private`, because `relayConnect()` uses it as its connect-time
    /// confirmation probe — a confirmation probe is this same question asked
    /// once, and #843's local copy showed what happens when the identity guard
    /// below is maintained in more than one place.
    func probe(_ task: URLSessionWebSocketTask, _ link: LivenessLink) {
        task.sendPing { [weak self] error in
            if let error {
                // Logged, not acted on. A failed ping is evidence, but not
                // proof: `sendPing` also reports a pong that merely timed out,
                // and recycling a link on one of those would churn a healthy
                // connection under load. `livenessStaleDeadline` is three ping
                // intervals precisely so a single lost pong is survivable — so
                // the deadline decides, and this line makes sure the signal is
                // not silently discarded on the way there.
                print("🔌 \(link.label) liveness ping failed: \(error.localizedDescription)")
                return
            }
            Task { @MainActor [weak self] in
                // Same staleness guard `connect()`'s confirmation ping uses: a
                // pong for a socket that has since been replaced must not stamp
                // the new one alive.
                guard let self, self.socketTask(for: link) === task else { return }
                self.recordLivenessPong(for: link)
            }
        }
    }
}
