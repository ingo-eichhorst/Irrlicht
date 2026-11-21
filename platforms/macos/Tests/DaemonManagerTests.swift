import XCTest
@testable import Irrlicht
import Foundation

@MainActor
final class DaemonManagerTests: XCTestCase {
    var manager: DaemonManager!

    override func setUp() async throws {
        try await super.setUp()
        manager = DaemonManager()
    }

    override func tearDown() async throws {
        manager.stop()
        manager = nil
        try await super.tearDown()
    }

    // MARK: - Health Check Tests

    func testDaemonNotReachableWhenNothingListening() async {
        // The probe goes through the process control. Before that seam this
        // test sent a real GET to whatever daemon was listening on the default
        // port — the developer's production daemon — and could not assert
        // anything about the answer. The recorder answers false by default.
        let control = RecordingDaemonProcessControl()
        let manager = DaemonManager(publisher: RecordingRelayPublisher(), processControl: control)
        let reachable = await manager.isDaemonReachable()
        XCTAssertFalse(reachable)
        XCTAssertEqual(control.calls, [.probe])
    }

    func testInitialState() {
        XCTAssertFalse(manager.daemonRunning)
    }

    func testStopIsIdempotent() {
        manager.stop()
        manager.stop()
        XCTAssertFalse(manager.daemonRunning)
    }

    // MARK: - Daemon launch env (issue #722)

    func testBuildDaemonEnvSetsBindAddrAndPreservesBase() {
        let env = DaemonManager.buildDaemonEnv(
            base: ["PATH": "/usr/bin"],  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            bindAddr: "127.0.0.1:7838"
        )
        XCTAssertEqual(env["IRRLICHT_BIND_ADDR"], "127.0.0.1:7838")
        XCTAssertEqual(env["PATH"], "/usr/bin", "base environment must be preserved")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
    }

    func testBuildDaemonEnvStripsInheritedRelayVars() {
        // Relay publishing travels over loopback now (issue #722), not launch
        // env, and the daemon self-seeds its forwarder from IRRLICHT_RELAY_URL at
        // boot. So buildDaemonEnv must both add no relay vars of its own AND strip
        // any inherited from the app's environment — otherwise an app-spawned
        // daemon would publish to a stale relay the user never configured until
        // the app's corrective PUT landed.
        let env = DaemonManager.buildDaemonEnv(
            base: ["IRRLICHT_RELAY_URL": "wss://stale", "IRRLICHT_RELAY_TOKEN": "stale", "PATH": "/usr/bin"],  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            bindAddr: "127.0.0.1:7837"
        )
        XCTAssertNil(env["IRRLICHT_RELAY_URL"], "inherited relay URL must be stripped")
        XCTAssertNil(env["IRRLICHT_RELAY_TOKEN"], "inherited relay token must be stripped")
        XCTAssertEqual(env["IRRLICHT_BIND_ADDR"], "127.0.0.1:7837")
        XCTAssertEqual(env["PATH"], "/usr/bin", "unrelated base vars must be preserved")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
    }

    func testPublishSettingsDidChangeDoesNotRelaunchDaemon() async {
        // The toggle now PUTs to the running daemon instead of relaunching it.
        // Under xctest there's no daemon binary and no app-owned process, so the
        // call must be a safe fire-and-forget — never spawn or crash. The
        // publish itself goes to an injected recorder: before the publisher
        // seam this test fired two real PUTs at whatever daemon was listening on
        // the default port, i.e. the developer's production daemon.
        let recorder = RecordingRelayPublisher()
        let defaults = InMemoryDefaults()
        defaults.set(true, forKey: "publishToRelay")
        defaults.set("  wss://relay.example.test  ", forKey: "relayServerURL")
        let keychain = KeychainSecrets(backend: InMemoryKeychainBackend())
        keychain.set(" tok-123 ", account: "relayToken")
        let manager = DaemonManager(publisher: recorder, defaults: defaults, keychain: keychain)
        defer { manager.stop() }

        XCTAssertNil(manager.currentProcessForTesting, "precondition: no app-owned daemon")
        manager.publishSettingsDidChange()
        manager.publishSettingsDidChange()
        XCTAssertFalse(manager.daemonRunning, "publishSettingsDidChange must not launch a daemon")
        XCTAssertNil(manager.currentProcessForTesting, "publishSettingsDidChange must not spawn a process")

        // The publish runs on a detached task: poll the recorder to a deadline
        // rather than sleeping, and fail with the elapsed time.
        let start = Date()
        while recorder.published.count < 2, Date().timeIntervalSince(start) < 5 {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        let elapsed = Date().timeIntervalSince(start)
        let expected = PublishClient.Config(enabled: true, url: "wss://relay.example.test", token: "tok-123")
        XCTAssertEqual(recorder.published, [expected, expected],
                       "two settings nudges must publish the trimmed current config twice "
                       + "(observed \(recorder.published.count) after \(String(format: "%.2f", elapsed))s)")
    }

    // MARK: - Bundle Path Tests

    func testBundledDaemonURLNilInTestHost() async {
        // start() on a missed probe kills stale daemons and spawns one. Live,
        // that kill is `pkill -x irrlichd` — every irrlichd on the machine,
        // production included — and the spawn launches core/bin/irrlichd when
        // a dev build exists. So the test drives a recorder and asserts on what
        // start() ASKED for, never on what happened to the machine.
        let control = RecordingDaemonProcessControl(reachable: false)
        let recorder = RecordingRelayPublisher()
        let manager = DaemonManager(publisher: recorder, processControl: control)
        defer { manager.stop() }

        manager.start()

        // Poll to a deadline for the spawn request rather than sleeping.
        let begin = Date()
        while !control.calls.contains(where: { if case .run = $0 { return true } else { return false } }),
              Date().timeIntervalSince(begin) < 5 {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        let elapsed = String(format: "%.2f", Date().timeIntervalSince(begin))

        XCTAssertEqual(Array(control.calls.prefix(3)),
                       [.probe, .killStaleDaemons, .run(executable: RecordingDaemonProcessControl.placeholderExecutable)],
                       "a missed probe must kill-then-spawn through the process control "
                       + "(calls after \(elapsed)s: \(control.calls))")
        let process = try? XCTUnwrap(manager.currentProcessForTesting)
        XCTAssertEqual(process?.executableURL, RecordingDaemonProcessControl.placeholderExecutable)
        XCTAssertEqual(process?.isRunning, false, "the recorder must never start the process it is handed")
    }

    func testStartAdoptsAReachableDaemonWithoutKillingOrSpawning() async {
        let control = RecordingDaemonProcessControl(reachable: true)
        let recorder = RecordingRelayPublisher()
        let manager = DaemonManager(publisher: recorder, processControl: control)
        defer { manager.stop() }

        manager.start()

        let begin = Date()
        while recorder.published.isEmpty, Date().timeIntervalSince(begin) < 5 {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        let elapsed = String(format: "%.2f", Date().timeIntervalSince(begin))

        XCTAssertEqual(recorder.published.count, 1,
                       "the adopt path publishes once (observed after \(elapsed)s)")
        XCTAssertEqual(control.calls, [.probe], "adopting must neither kill nor spawn")
        XCTAssertTrue(manager.daemonRunning)
        XCTAssertNil(manager.currentProcessForTesting)
    }
}
