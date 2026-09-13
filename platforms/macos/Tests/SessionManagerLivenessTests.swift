import XCTest
@testable import Irrlicht
import Foundation
import AppKit

/// #1953. The Mac wakes from standby, the relay socket is left half-open, and
/// nothing ever notices: `receive()` waits forever, so the backoff reconnect
/// behind it never runs, and `relayConnectionState` — set eagerly one line
/// after `resume()` — sits on `.connected` with no data behind it. Only
/// toggling the relay Source off and on recovered.
///
/// These pin the two seams that close it: the repeating liveness tick and
/// `handleSystemDidWake()`. Both are new, so neither has a "before the fix" to
/// run red — each test below carries the mutation that was applied to the
/// source to see it go red, written from the mutation as run.
///
/// No socket is opened anywhere in this file. The tasks handed to the manager
/// are built against a port nothing listens on and are never `resume()`d, so
/// they are inert objects with the right identity — which is all the code under
/// test reads from them.
@MainActor
final class SessionManagerLivenessTests: XCTestCase {
    private var sut: SessionManager!

    /// Long enough that no test here can be flaky on a slow machine, short
    /// enough that a genuine failure is reported quickly rather than hanging
    /// the suite.
    private let pollDeadline: TimeInterval = 5.0

    override func setUp() async throws {
        try await super.setUp()
        sut = SessionManager(defaults: InMemoryDefaults())
    }

    override func tearDown() async throws {
        sut = nil
        try await super.tearDown()
    }

    /// An inert `URLSessionWebSocketTask`: never resumed, and pointed at a
    /// loopback port nothing listens on, so even an accidental dial fails
    /// instantly and locally.
    private func inertTask() -> URLSessionWebSocketTask {
        URLSession(configuration: .ephemeral)
            .webSocketTask(with: URL(string: "ws://127.0.0.1:1/api/v1/sessions/stream")!)
    }

    /// Polls `condition` to a deadline and fails with the elapsed time. Never
    /// sleeps blind: both effects observed here — a notification reaching the
    /// handler, and a cancelled `URLSessionTask` settling — are asynchronous,
    /// so a fixed wait would either be flaky or slower than the whole suite.
    private func poll(_ description: String,
                      until condition: () -> Bool,
                      file: StaticString = #filePath,
                      line: UInt = #line) async {
        let started = Date()
        while Date().timeIntervalSince(started) < pollDeadline {
            if condition() { return }
            await Task.yield()
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTFail("\(description) did not happen within \(String(format: "%.2f", Date().timeIntervalSince(started)))s",
                file: file, line: line)
    }

    /// A cancelled `URLSessionTask` ends at `.completed`; one that was never
    /// touched stays at `.suspended` (it was never resumed), so the two are
    /// cleanly distinguishable. The transition is asynchronous — measured:
    /// reading `state` on the line after `cancel()` returned `.suspended` for
    /// one link and `.completed` for the other in the same test — so it is
    /// polled rather than read once.
    private func assertCancelled(_ task: URLSessionWebSocketTask,
                                 _ description: String,
                                 file: StaticString = #filePath,
                                 line: UInt = #line) async {
        await poll(description, until: { task.state == .completed }, file: file, line: line)
    }

    // MARK: - The liveness tick

    /// The defect, expressed at the seam that fixes it: a relay link that has
    /// been silent past the deadline gets its socket dropped, which is the one
    /// thing `relayConnect()`'s parked `receive()` needs in order to throw and
    /// run the backoff reconnect that already exists.
    ///
    /// Mutation, applied to the source and run: no-op the reap branch in
    /// `SessionManager+Liveness.tickLink` by deleting its
    /// `setLastFrameAt(nil, for: link)` and `task.cancel()`. Observed: this
    /// test fails twice — `XCTAssertNil failed` on the stamp, then the cancel
    /// poll times out with "did not happen within 5.00s" — and
    /// `testTickDropsALocalSocketThatHasGoneSilent` fails with it.
    func testTickDropsARelaySocketThatHasGoneSilent() async {
        let task = inertTask()
        sut.relayWebSocketTask = task
        sut.relayConnectionState = .connected
        let now = Date()
        sut.lastRelayFrameAt = now.addingTimeInterval(-(sut.livenessStaleDeadline + 1))

        sut.checkConnectionLiveness(now: now)

        XCTAssertEqual(sut.livenessTeardowns, [.stale(.relay)],
                       "a link silent past the deadline must be torn down, and recorded as stale")
        XCTAssertNil(sut.lastRelayFrameAt,
                     "the stamp must be cleared with the socket, or the next tick reaps the replacement")
        await assertCancelled(task, "a link silent past the deadline having its socket dropped")
    }

    /// The same for the local link, which shares `tickLink`.
    ///
    /// Mutation, applied to the source and run: delete the `tickLink(.local,
    /// now: now)` call from `checkConnectionLiveness`. Observed: this test is
    /// the only one in the file that fails, and the relay test above stays
    /// green — which is exactly the shape of a fix wired to one link only.
    func testTickDropsALocalSocketThatHasGoneSilent() async {
        let task = inertTask()
        sut.webSocketTask = task
        sut.connectionState = .connected
        let now = Date()
        sut.lastLocalFrameAt = now.addingTimeInterval(-(sut.livenessStaleDeadline + 1))

        sut.checkConnectionLiveness(now: now)

        XCTAssertEqual(sut.livenessTeardowns, [.stale(.local)])
        XCTAssertNil(sut.lastLocalFrameAt)
        await assertCancelled(task, "a link silent past the deadline having its socket dropped")
    }

    /// The other half of the guard, and the one a too-eager reap would break: a
    /// link that spoke recently is left alone.
    ///
    /// Mutation, applied to the source and run: negate the `isStale` condition
    /// in `tickLink`. Observed: this test fails on the stamp and the teardown
    /// record, and takes the two reap tests down with it — a tick that recycled
    /// every healthy connection every 20s.
    func testTickLeavesAFreshLinkAlone() {
        let task = inertTask()
        sut.relayWebSocketTask = task
        sut.relayConnectionState = .connected
        let now = Date()
        let stamp = now.addingTimeInterval(-1)
        sut.lastRelayFrameAt = stamp

        sut.checkConnectionLiveness(now: now)

        // Both observations are synchronous and unambiguous: the manager
        // records every teardown it performs, and the reap clears the stamp on
        // the line before it cancels. Neither can be confused with "the cancel
        // has not landed yet", which is what reading `task.state` here would
        // be — measured during review, `state` still reads `.suspended` on the
        // line after `cancel()`.
        XCTAssertTrue(sut.livenessTeardowns.isEmpty, "a fresh link must not be dropped")
        XCTAssertEqual(sut.lastRelayFrameAt, stamp, "a fresh link's stamp must survive the tick")
    }

    /// **Red-first, against the first cut of this very change.** A socket that
    /// completes its handshake and then goes silent never delivers a first
    /// frame — so nothing ever stamped it, and a tick that reads a nil stamp as
    /// "not stale" would leave it wedged forever. That is #1953's own failure
    /// mode moved one connect earlier, and a post-wake reconnect is exactly how
    /// you reach it: `handleSystemDidWake()` nils both stamps and cancels both
    /// tasks, and the replacement socket handshakes against a peer that is
    /// already gone.
    ///
    /// The tick therefore starts a clock for any socket that has none, rather
    /// than granting it immortality. Reaping on the same tick is wrong — there
    /// would be no elapsed time to judge — so the first tick starts the clock
    /// and a later one reaps, which costs at most one `livenessPingInterval`.
    func testTickStartsAClockForASocketThatHasNoneAndLaterReapsIt() async {
        let task = inertTask()
        sut.relayWebSocketTask = task
        sut.relayConnectionState = .connecting
        sut.lastRelayFrameAt = nil
        let t0 = Date()

        sut.checkConnectionLiveness(now: t0)

        XCTAssertEqual(sut.lastRelayFrameAt, t0,
                       "a socket with no liveness clock must be given one, not left immortal")
        XCTAssertTrue(sut.livenessTeardowns.isEmpty,
                      "the tick that starts the clock has no elapsed time to judge, so it must not reap")

        sut.checkConnectionLiveness(now: t0.addingTimeInterval(sut.livenessStaleDeadline + 1))

        XCTAssertEqual(sut.livenessTeardowns, [.stale(.relay)],
                       "a link that dialled and never spoke must be reaped once the deadline passes")
        await assertCancelled(task, "the never-confirmed socket being dropped")
    }

    /// With nothing connected the tick must do nothing at all — this is what
    /// makes it safe to arm in every constructed manager, including under
    /// `swift test`.
    func testTickIsANoOpWithNoLiveLinks() {
        sut.webSocketTask = nil
        sut.relayWebSocketTask = nil

        sut.checkConnectionLiveness(now: Date().addingTimeInterval(86_400))

        XCTAssertNil(sut.lastLocalFrameAt, "no socket means no clock to start")
        XCTAssertNil(sut.lastRelayFrameAt, "no socket means no clock to start")
        XCTAssertTrue(sut.livenessTeardowns.isEmpty)
    }

    // MARK: - Pong confirmation

    /// A pong is not just a stamp, it is a confirmation — and without that this
    /// change would have introduced a bug of its own.
    ///
    /// Moving `.connected` out of `relayConnect()` left the relay with only one
    /// confirmation path, an arrived frame. But `relayServerURL` is allowed to
    /// point at a bare `irrlichd` rather than an `irrlichtrelay`
    /// (`SessionManager+Relay.swift`'s hello comment says so, and
    /// `handleRelayMessage`'s `default:` branch decodes raw daemon frames), and
    /// that daemon writes one snapshot per known session and then goes idle —
    /// nothing at all when it has none. The link would then sit `.connecting`
    /// forever on a healthy socket: amber dot, no per-daemon tooltip lines, and
    /// — since `aggregateConnectionState` never reaches `.connected` —
    /// `DaemonHealth`'s mask off, so three drops would raise a false "The relay
    /// server is not responding". This is `connect()`'s own #843 reasoning,
    /// which the relay path had never needed until now.
    ///
    /// Mutation, applied to the source and run: drop the `switch` from
    /// `recordLivenessPong(for:)`, leaving only the stamp. Observed: this test
    /// and `testAPongConfirmsTheLocalLink` fail on the state assertion.
    func testAPongConfirmsTheRelayLink() {
        sut.relayConnectionState = .connecting
        sut.lastRelayFrameAt = nil

        sut.recordLivenessPong(for: .relay)

        XCTAssertEqual(sut.relayConnectionState, .connected,
                       "a pong proves the relay answered, even when it has no frames to send")
        XCTAssertNotNil(sut.lastRelayFrameAt)
    }

    func testAPongConfirmsTheLocalLink() {
        sut.connectionState = .connecting
        sut.lastLocalFrameAt = nil

        sut.recordLivenessPong(for: .local)

        XCTAssertEqual(sut.connectionState, .connected)
        XCTAssertNotNil(sut.lastLocalFrameAt)
    }

    /// The stamp must be refreshed on every pong, not only on the one that
    /// confirms — otherwise an idle-but-healthy link would be reaped at the
    /// deadline despite answering every ping.
    func testAPongOnAnAlreadyConnectedLinkStillRefreshesTheStamp() {
        sut.relayConnectionState = .connected
        let old = Date().addingTimeInterval(-30)
        sut.lastRelayFrameAt = old

        sut.recordLivenessPong(for: .relay)

        XCTAssertNotEqual(sut.lastRelayFrameAt, old,
                          "an idle link stays alive only because its pongs keep re-stamping it")
    }

    /// `relayConnect()` must arm a confirmation probe on every fresh socket.
    ///
    /// This is a SOURCE check, not a behavioural one, and that is a deliberate
    /// second-best: `relayConnect()` opens a real socket, so no test in this
    /// suite can call it (`SessionManagerTests`' #832 note, and the reason
    /// `connect()`'s own identical confirmation ping has never had a test
    /// either). What the tests above DO cover is the probe's effect — a pong
    /// confirms the link (`testAPongConfirmsTheRelayLink`). What was uncovered
    /// until this test is the wiring: deleting the `probe(task, .relay)` line
    /// left the whole suite green, while shipping a relay pointed at a bare
    /// `irrlichd` that sits `.connecting` forever.
    ///
    /// Fails loudly when it cannot look: an unreadable file, or a
    /// `relayConnect()` it cannot find, is a failure and not a silent pass.
    func testRelayConnectArmsAConfirmationProbe() throws {
        let source = URL(fileURLWithPath: #filePath)          // …/Tests/<this file>
            .deletingLastPathComponent()                      // …/Tests
            .deletingLastPathComponent()                      // …/platforms/macos
            .appendingPathComponent("Irrlicht/Managers/SessionManager+Relay.swift")
        let text = try String(contentsOf: source, encoding: .utf8)
        XCTAssertFalse(text.isEmpty, "read SessionManager+Relay.swift but it was empty")

        guard let body = text.range(of: "func relayConnect() async {") else {
            return XCTFail("could not find relayConnect() in \(source.path) — this check could not run")
        }
        let afterResume = text[body.upperBound...]
        guard let end = afterResume.range(of: "\n    }\n") else {
            return XCTFail("could not find the end of relayConnect() — this check could not run")
        }

        XCTAssertTrue(afterResume[..<end.lowerBound].contains("probe(task, .relay)"),
                      "relayConnect() must arm a confirmation probe, or a relay that sends no "
                          + "frames of its own never leaves .connecting (#1953)")
    }

    // MARK: - Frame stamping

    /// The tick can only judge silence if something records speech. Both
    /// inbound handlers stamp before they decode, so a frame this build cannot
    /// parse still counts as the socket carrying bytes.
    ///
    /// Mutation, applied to the source and run: delete the `lastRelayFrameAt =
    /// Date()` line from `handleRelayMessage`. Observed: this test and
    /// `testAnUndecodableRelayFrameStillStampsTheLink` both fail on
    /// `XCTAssertNotNil` — and with them the whole mechanism, since a busy
    /// relay would then look silent after 60s and be recycled on a loop.
    func testAnArrivedRelayFrameStampsTheLink() {
        sut.lastRelayFrameAt = nil

        sut.handleRelayMessage(#"{"type":"hello_ack"}"#)

        XCTAssertNotNil(sut.lastRelayFrameAt, "an arrived relay frame must stamp the link alive")
    }

    /// Same, for a payload no version of this app can decode.
    func testAnUndecodableRelayFrameStillStampsTheLink() {
        sut.lastRelayFrameAt = nil

        sut.handleRelayMessage("this is not JSON")

        XCTAssertNotNil(sut.lastRelayFrameAt,
                        "a frame the decoder rejects is still proof the socket carried bytes")
    }

    /// Mutation, applied to the source and run: delete the `lastLocalFrameAt =
    /// Date()` line from `handleWsMessage`. Observed: this test is the only one
    /// that fails.
    func testAnArrivedLocalFrameStampsTheLink() {
        sut.lastLocalFrameAt = nil

        sut.handleWsMessage(#"{"type":"ping"}"#)

        XCTAssertNotNil(sut.lastLocalFrameAt, "an arrived local frame must stamp the link alive")
    }

    /// A stamp must not outlive the link it describes.
    func testStoppingALinkClearsItsStamp() {
        sut.lastLocalFrameAt = Date()
        sut.lastRelayFrameAt = Date()

        sut.stopWebSocket()
        sut.stopRelay()

        XCTAssertNil(sut.lastLocalFrameAt)
        XCTAssertNil(sut.lastRelayFrameAt)
    }

    // MARK: - System wake

    /// Called directly, as the seam it is: a wake drops every socket that was
    /// open across the sleep and resets both backoffs, so the reconnect each
    /// cancel releases dials in ~1s instead of at whatever delay the link had
    /// backed off to.
    ///
    /// Mutation, applied to the source and run: delete the two backoff resets
    /// and the two stamp clears from `handleSystemDidWake()`, leaving only the
    /// cancels. Observed: this test fails on all four of those assertions
    /// (`30.0` is not equal to `1.0`, twice; `XCTAssertNil failed`, twice),
    /// and `testWakeWithNoSocketsIsHarmless` and
    /// `testAWakeNotificationRecoversBothLinks` fail with it.
    func testWakeDropsBothSocketsAndResetsBothBackoffs() async {
        let local = inertTask()
        let relay = inertTask()
        sut.webSocketTask = local
        sut.relayWebSocketTask = relay
        sut.connectionState = .connected
        sut.relayConnectionState = .connected
        sut.lastLocalFrameAt = Date()
        sut.lastRelayFrameAt = Date()
        sut.reconnectDelay = 30.0
        sut.relayReconnectDelay = 30.0

        sut.handleSystemDidWake()

        XCTAssertEqual(sut.livenessTeardowns, [.wake(.local), .wake(.relay)],
                       "a wake must drop BOTH sockets, and record each as a wake teardown")
        await assertCancelled(local, "the local socket being dropped on wake")
        await assertCancelled(relay, "the relay socket being dropped on wake")
        XCTAssertEqual(sut.reconnectDelay, 1.0, "the local backoff must be reset so the re-dial is immediate")
        XCTAssertEqual(sut.relayReconnectDelay, 1.0, "the relay backoff must be reset so the re-dial is immediate")
        XCTAssertNil(sut.lastLocalFrameAt)
        XCTAssertNil(sut.lastRelayFrameAt)
    }

    // NOT ASSERTED, deliberately, and recorded here rather than left to be
    // rediscovered: that a liveness teardown uses a plain `cancel()` and not
    // `cancel(with:reason:)` — the property `SessionManager+Liveness.dropSocket`
    // depends on, since `relayConnect()` reads `task.closeCode` to tell a 4401
    // token rejection from an ordinary drop.
    //
    // An earlier draft of this suite asserted `task.closeCode == .invalid` after
    // the wake and was green. It was green for the wrong reason. Measured during
    // this change's review, on a `URLSessionWebSocketTask` that never completed
    // a handshake (which is every task in this file): `closeCode` reads
    // `.invalid` after `cancel()`, after `cancel(with: .normalClosure,
    // reason: nil)`, and with no cancel at all — 8/8 runs. A close code is only
    // ever set by a close frame crossing a live socket, so the assertion passed
    // under exactly the mutation it existed to catch, and equally under "delete
    // the cancels entirely".
    //
    // Asserting it for real needs a loopback WebSocket server, which no other
    // test here requires. A stated gap beats a green that proves nothing.

    /// A wake with nothing connected must not invent state.
    func testWakeWithNoSocketsIsHarmless() {
        sut.webSocketTask = nil
        sut.relayWebSocketTask = nil
        sut.relayConnectionStalled = true

        sut.handleSystemDidWake()

        XCTAssertFalse(sut.relayConnectionStalled, "the backoff reset clears the stalled flag")
        XCTAssertNil(sut.relayWebSocketTask)
        XCTAssertTrue(sut.livenessTeardowns.isEmpty, "there was nothing to tear down")
    }

    // MARK: - Wiring

    /// The observer is registered in `init`, so every manager the app builds
    /// has one.
    ///
    /// Mutation, applied to the source and run: delete the
    /// `observeSystemWake()` call from `SessionManager.init`. Observed: this
    /// test is the only one that fails.
    func testInitRegistersASystemWakeObserver() {
        XCTAssertNotNil(sut.systemWakeObserver,
                        "init must bind the wake handler, or a wake reaches nothing (#1953)")
        XCTAssertTrue(sut.systemWakeCenter === NSWorkspace.shared.notificationCenter,
                      "it must be the workspace's own center — a real wake is posted nowhere else")
    }

    /// The tick is armed in `init` too.
    ///
    /// Mutation, applied to the source and run: delete the
    /// `startConnectionLivenessPolling()` call from `SessionManager.init`.
    /// Observed: this test is the only one that fails, on both assertions — the
    /// liveness machinery would exist and never run, which is the failure mode
    /// this whole change exists to prevent one layer down.
    func testInitArmsTheLivenessTimer() {
        XCTAssertNotNil(sut.livenessTimer, "init must arm the liveness tick")
        XCTAssertEqual(sut.livenessTimer?.timeInterval, sut.livenessPingInterval)
    }

    /// The deadline must stay above `irrlichtrelay`'s own 45s pong timeout
    /// (`core/cmd/irrlichtrelay/hub.go:23`) so the client never declares a link
    /// dead before the server would have reaped its half, and the ping must
    /// stay below the relay's 30s ping interval (`:22`). A lock: it pins the
    /// two values chosen in triage, and passes by construction.
    func testTheChosenIntervalsStraddleTheRelaysOwnTimeouts() {
        XCTAssertLessThan(sut.livenessPingInterval, 30.0,
                          "the client must probe at least as often as the relay does")
        XCTAssertGreaterThan(sut.livenessStaleDeadline, 45.0,
                             "the client must not declare death before the relay's own pongTimeout")
        XCTAssertGreaterThanOrEqual(sut.livenessStaleDeadline, sut.livenessPingInterval * 3,
                                    "one dropped pong must not be fatal")
    }

    /// Registering twice replaces the observer rather than stacking a second
    /// one, so a re-registration cannot double-fire the handler.
    func testReRegisteringReplacesTheObserver() {
        let first = sut.systemWakeObserver
        let center = NotificationCenter()

        sut.observeSystemWake(center)

        XCTAssertNotNil(sut.systemWakeObserver)
        XCTAssertFalse(sut.systemWakeObserver === first, "the observer must be replaced, not stacked")
        XCTAssertTrue(sut.systemWakeCenter === center, "and re-pointed at the new center")
    }

    /// The teardown history is bounded, so an app left running for weeks does
    /// not accumulate one entry per reap. Without this the trim branch in
    /// `dropSocket` would never execute in the suite at all.
    func testTheTeardownHistoryIsBounded() {
        let overshoot = sut.livenessTeardownHistoryLimit + 4
        for _ in 0..<overshoot {
            let task = inertTask()
            sut.relayWebSocketTask = task
            let now = Date()
            sut.lastRelayFrameAt = now.addingTimeInterval(-(sut.livenessStaleDeadline + 1))
            sut.checkConnectionLiveness(now: now)
        }

        XCTAssertEqual(sut.livenessTeardowns.count, sut.livenessTeardownHistoryLimit,
                       "the history must be capped, not grow with uptime")
        XCTAssertEqual(sut.livenessTeardowns, Array(repeating: .stale(.relay),
                                                    count: sut.livenessTeardownHistoryLimit),
                       "and it must keep the NEWEST entries, dropping the oldest")
    }

    /// End-to-end over the REAL notification name. This is the one test that
    /// goes through the notification rather than calling the seam; it exists
    /// because a handler bound to the wrong name is indistinguishable from no
    /// handler at all, and no direct call can see that.
    ///
    /// The `name:` argument is deliberately omitted, so the thing under test is
    /// the genuine `NSWorkspace.didWakeNotification` default and not a
    /// test-only name. The CENTER is overridden, and that is not a weakening:
    /// posting on `NSWorkspace.shared.notificationCenter` would run
    /// `handleSystemDidWake()` on every other `SessionManager` alive in the test
    /// process — each one registers there in `init` and only `deinit`
    /// unregisters — cancelling their sockets and resetting their backoffs. The
    /// center default is pinned instead by
    /// `testInitRegistersASystemWakeObserver`, so both defaults stay covered
    /// without one test reaching into every other.
    ///
    /// Mutation, applied to the source and run: change the `name:` default in
    /// `observeSystemWake` to `NSWorkspace.willSleepNotification`. Observed:
    /// this test fails with the poll's elapsed time, and it is the only one
    /// that does — every direct-call test above stays green, which is the point
    /// of having this one.
    func testAWakeNotificationRecoversBothLinks() async {
        let center = NotificationCenter()
        sut.observeSystemWake(center)
        sut.relayReconnectDelay = 30.0
        sut.lastRelayFrameAt = Date()

        center.post(name: NSWorkspace.didWakeNotification, object: nil)

        await poll("the posted NSWorkspace.didWakeNotification reaching handleSystemDidWake()") {
            self.sut.relayReconnectDelay == 1.0 && self.sut.lastRelayFrameAt == nil
        }
    }
}
