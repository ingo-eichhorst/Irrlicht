import Combine
import XCTest
@testable import Irrlicht

/// #2145 review: the hydrate that the 2.5 s orchestrator cadence drives,
/// exercised end to end against a stubbed /api/v1/sessions. Each request is
/// held by `HeldSessionsProtocol` until the test answers it, in any order.
///
/// Red-first: the failed-hydrate, older-response and unchanged-poll tests
/// failed before their fixes. The cadence-follows-the-groups test passed
/// there by construction; it is what a deleted or inverted re-arm turns red.
@MainActor
final class SessionManagerHydrateCadenceTests: XCTestCase {
    private var sut: SessionManager!
    private var store: InMemoryDefaults!

    override func setUp() async throws {
        try await super.setUp()
        HeldSessionsProtocol.reset()
        store = InMemoryDefaults()
        store.set(true, forKey: "useLocalDaemon")
        sut = SessionManager(defaults: store)
        sut.localURLSession = HeldSessionsProtocol.session()
    }

    override func tearDown() async throws {
        sut = nil
        HeldSessionsProtocol.reset()
        try await super.tearDown()
    }

    private static func pool(_ state: String) -> Data {
        Data("""
        {"groups":[{"name":"VM tasks","type":"workerlist","groups":[{"name":"lead-T100","status":"operational",
          "unattached_workers":[{"role":"worker","worker_name":"T101","worker_state":"\(state)"}]}]}]}
        """.utf8)
    }

    private static let noOrchestrator = Data(#"{"groups":[{"name":"irrlicht","agents":[]}]}"#.utf8)

    /// Starts a hydrate and waits until its request is held as number `n`.
    private func startHydrate(_ trigger: SessionManager.HydrateTrigger = .explicit,
                              heldAs n: Int) async -> Task<Void, Never> {
        let task = Task { await sut.hydrateSessions(trigger: trigger) }
        let deadline = Date().addingTimeInterval(5)
        while HeldSessionsProtocol.heldCount < n {
            if Date() > deadline {
                XCTFail("request \(n) never reached the stub within 5 s (held: \(HeldSessionsProtocol.heldCount))")
                break
            }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        return task
    }

    private func hydrate(answering body: Data, trigger: SessionManager.HydrateTrigger = .explicit) async {
        let n = HeldSessionsProtocol.heldCount + 1
        let task = await startHydrate(trigger, heldAs: n)
        HeldSessionsProtocol.answer(n - 1, body: body)
        await task.value
    }

    private var t101State: String? {
        sut.localApiGroups.first?.groups?.first?.unattachedWorkers?.first?.workerState
    }

    /// The cadence follows what each successful hydrate installed: 2.5 s
    /// while an orchestrator is listed, 30 s once none is.
    func testTheCadenceFollowsTheInstalledGroups() async {
        sut.startProjectCostsPolling()
        XCTAssertEqual(sut.armedHydrateInterval, 30)
        await hydrate(answering: Self.pool("busy"))
        XCTAssertEqual(sut.armedHydrateInterval, 2.5, "a listed orchestrator did not speed the hydrate up")
        await hydrate(answering: Self.noOrchestrator)
        XCTAssertEqual(sut.armedHydrateInterval, 30, "the hydrate stayed fast after the orchestrator left")
    }

    /// A daemon that answers with an error, or not at all, is not polled
    /// every 2.5 s: the hydrate falls back to the idle cadence.
    func testAFailedHydrateFallsBackToTheIdleCadence() async {
        sut.startProjectCostsPolling()
        await hydrate(answering: Self.pool("busy"))
        XCTAssertEqual(sut.armedHydrateInterval, 2.5, "precondition")

        let refused = await startHydrate(heldAs: 2)
        HeldSessionsProtocol.answer(1, status: 500, body: Data())
        await refused.value
        XCTAssertEqual(sut.armedHydrateInterval, 30, "a non-200 hydrate kept the 2.5 s cadence")

        await hydrate(answering: Self.pool("busy"))
        let unreachable = await startHydrate(heldAs: 4)
        HeldSessionsProtocol.fail(3)
        await unreachable.value
        XCTAssertEqual(sut.armedHydrateInterval, 30, "an unreachable daemon kept the 2.5 s cadence")
    }

    /// Two overlapping hydrates whose responses arrive out of order: the
    /// older one must not be installed over the newer one.
    func testAnOlderResponseIsNotInstalledOverANewerOne() async {
        let older = await startHydrate(heldAs: 1)
        let newer = await startHydrate(heldAs: 2)
        HeldSessionsProtocol.answer(1, body: Self.pool("waiting"))
        await newer.value
        XCTAssertEqual(t101State, "waiting", "precondition: the newer response installed")
        HeldSessionsProtocol.answer(0, body: Self.pool("busy"))
        await older.value
        XCTAssertEqual(t101State, "waiting", "the older response was installed over the newer one")
    }

    /// A timer poll whose body is byte-identical to the last one installed
    /// republishes nothing.
    func testAnUnchangedTimerPollRepublishesNothing() async {
        await hydrate(answering: Self.pool("busy"))
        var published = 0
        let sink = sut.$apiGroups.dropFirst().sink { _ in published += 1 }
        defer { sink.cancel() }
        await hydrate(answering: Self.pool("busy"), trigger: .timer)
        XCTAssertEqual(published, 0, "an unchanged timer poll republished apiGroups")
        await hydrate(answering: Self.pool("waiting"), trigger: .timer)
        XCTAssertEqual(published, 1, "a changed timer poll was not installed")
    }
}

/// Answers /api/v1/sessions from the test: each request is held until the
/// test answers or fails it, by arrival index.
final class HeldSessionsProtocol: URLProtocol {
    private static let lock = NSLock()
    private static var held: [HeldSessionsProtocol] = []

    static var heldCount: Int { lock.lock(); defer { lock.unlock() }; return held.count }
    static func reset() { lock.lock(); held.removeAll(); lock.unlock() }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [HeldSessionsProtocol.self]
        return URLSession(configuration: config)
    }

    private static func request(_ index: Int) -> HeldSessionsProtocol {
        lock.lock(); defer { lock.unlock() }
        return held[index]
    }

    static func answer(_ index: Int, status: Int = 200, body: Data) {
        let p = request(index)
        let response = HTTPURLResponse(url: p.request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        p.client?.urlProtocol(p, didReceive: response, cacheStoragePolicy: .notAllowed)
        p.client?.urlProtocol(p, didLoad: body)
        p.client?.urlProtocolDidFinishLoading(p)
    }

    static func fail(_ index: Int) {
        let p = request(index)
        p.client?.urlProtocol(p, didFailWithError: URLError(.cannotConnectToHost))
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() { Self.lock.lock(); Self.held.append(self); Self.lock.unlock() }
    override func stopLoading() {}
}
