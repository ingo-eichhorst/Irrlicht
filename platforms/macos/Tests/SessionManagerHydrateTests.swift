import XCTest
@testable import Irrlicht

/// #2145: the hydrate that installs orchestrator state, exercised end to end
/// against a stubbed /api/v1/sessions. Each request is held by
/// `HeldSessionsProtocol` until the test answers it, in any order.
///
/// Red-first: both tests failed before their fixes — the push test before
/// `orchestrator_updated` scheduled a rehydrate, the order test before
/// `hydrateSessions` dropped a response older than the last one installed.
@MainActor
final class SessionManagerHydrateTests: XCTestCase {
    private var sut: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        HeldSessionsProtocol.reset()
        let store = InMemoryDefaults()
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
        let waiting = state == "waiting" ? #","waiting":true"# : ""
        return Data("""
        {"groups":[{"name":"VM tasks","type":"workerlist","groups":[{"name":"lead-T100","status":"operational",
          "unattached_workers":[{"role":"worker","worker_name":"T101","worker_state":"\(state)"\(waiting)}]}]}]}
        """.utf8)
    }

    /// Waits until `n` requests are held; on timeout fails with the elapsed
    /// time and returns false, so the caller answers no request that never
    /// arrived.
    private func waitForHeld(_ n: Int, what: String) async -> Bool {
        let start = Date()
        while HeldSessionsProtocol.heldCount < n {
            if Date().timeIntervalSince(start) > 5 {
                XCTFail("\(what): \(HeldSessionsProtocol.heldCount) of \(n) requests after \(Date().timeIntervalSince(start))s")
                return false
            }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        return true
    }

    private var t101State: String? {
        sut.localApiGroups.first?.groups?.first?.unattachedWorkers?.first?.workerState
    }

    /// An orchestrator_updated push re-fetches /api/v1/sessions: the daemon
    /// sends it when an orchestrator's shown state changes, which no other
    /// WebSocket message carries.
    func testAnOrchestratorUpdatePushRefetchesTheSessions() async {
        sut.handleWsMessage(#"{"type":"orchestrator_updated"}"#)
        guard await waitForHeld(1, what: "orchestrator_updated did not re-fetch /api/v1/sessions") else { return }
        HeldSessionsProtocol.answer(0, body: Self.pool("waiting"))
    }

    /// LOCK: permissions_updated still re-fetches the permissions after it
    /// moved, with orchestrator_updated, into handleDatalessFrame (#2145).
    func testAPermissionsUpdatePushStillRefetchesThePermissions() async {
        sut.handleWsMessage(#"{"type":"permissions_updated"}"#)
        guard await waitForHeld(1, what: "permissions_updated did not re-fetch") else { return }
        XCTAssertEqual(HeldSessionsProtocol.path(0), "/api/v1/permissions")
        HeldSessionsProtocol.answer(0, status: 500, body: Data())
    }

    /// Two overlapping hydrates whose responses arrive out of order: the
    /// older one must not be installed over the newer one.
    func testAnOlderResponseIsNotInstalledOverANewerOne() async {
        let older = Task { await sut.hydrateSessions() }
        guard await waitForHeld(1, what: "the first hydrate") else { return }
        let newer = Task { await sut.hydrateSessions() }
        guard await waitForHeld(2, what: "the second hydrate") else { return }
        HeldSessionsProtocol.answer(1, body: Self.pool("waiting"))
        await newer.value
        XCTAssertEqual(t101State, "waiting", "precondition: the newer response installed")
        HeldSessionsProtocol.answer(0, body: Self.pool("busy"))
        await older.value
        XCTAssertEqual(t101State, "waiting", "the older response was installed over the newer one")
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

    static func path(_ index: Int) -> String? { request(index).request.url?.path }

    static func fail(_ index: Int) {
        let p = request(index)
        p.client?.urlProtocol(p, didFailWithError: URLError(.cannotConnectToHost))
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() { Self.lock.lock(); Self.held.append(self); Self.lock.unlock() }
    override func stopLoading() {}
}
