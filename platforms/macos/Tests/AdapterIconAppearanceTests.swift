import XCTest
import SwiftUI
@testable import Irrlicht

/// Issue #1509 — the adapter brand icon must follow the appearance of the
/// *view it is rendered into*, not the appearance of the host process.
///
/// `SessionState.adapterIcon` used to resolve its light/dark SVG variant from
/// the global `NSApp.effectiveAppearance`. Every caller is a SwiftUI view that
/// can be rendered under a pinned appearance — the snapshot suite pins
/// `.darkAqua` on its `NSHostingView` — so the icon silently disagreed with the
/// row it sat in, and on a machine with macOS auto-appearance
/// (`AppleInterfaceStyleSwitchesAutomatically`) the rendered pixels changed
/// with the time of day. That is what made
/// `testGhostRowPID0NilMetrics` / `testFixtureAntigravityGhost` red at night
/// and green by day, and it was twice misdiagnosed as toolchain
/// antialiasing drift and "fixed" by regenerating the references (#1034,
/// #1044) — which only re-pins whichever variant the machine happened to be
/// in. See the reference PNG history: LIGHT (ade90bdc) → DARK (b7e33c06) →
/// LIGHT (e77e3a83). It oscillates; AA drift would not.
///
/// The invariant asserted here is deliberately independent of the machine's
/// current system appearance: it renders the *same* session twice, once in a
/// dark-pinned host and once in a light-pinned host, and compares the two.
/// While the icon tracked `NSApp`, both renders picked the same variant, so
/// the comparison failed whichever way the system was set.
@MainActor
final class AdapterIconAppearanceTests: XCTestCase {
    private var sessionManager: SessionManager!
    private var savedRegistry: [String: AgentBranding] = [:]

    /// This suite's own preference store (#1662). It replaces a six-key
    /// snapshot-and-restore over the REAL `com.apple.dt.xctest.tool` domain,
    /// which depended on a `tearDown` that #1523's aborts, the 240s tree kill
    /// and `--budget` all skip. Empty is the right content: every key
    /// `SessionRowView` reads then resolves at its own `@AppStorage` default,
    /// which is what the deleted dictionary was assigning. `debugMode` is the
    /// one that matters most here — it adds a line to the row's VStack inside a
    /// fixed-height frame, shifting the metrics line up and out of the icon
    /// probe box — and it defaults to `false`.
    private let defaults = InMemoryDefaults()

    override func setUp() async throws {
        try await super.setUp()
        sessionManager = SessionManager(defaults: defaults)
        savedRegistry = AgentRegistry.byName
        AgentRegistry.byName["antigravity"] = TestAgentBranding.antigravity
    }

    override func tearDown() async throws {
        AgentRegistry.byName = savedRegistry
        sessionManager = nil
        try await super.tearDown()
    }

    // MARK: - The invariant

    /// Renders one antigravity row twice — dark-pinned and light-pinned — and
    /// asserts each host shows its own variant of the brand mark.
    ///
    /// The discriminator is the *blueness* (B − R) of the bluest pixel in the
    /// icon slot. Antigravity's leading segment is `#4285F4` in the light
    /// variant (B − R = 178) and `#8AB4F8` in the dark one (B − R = 110); the
    /// light variant is markedly bluer whichever background it is composited
    /// over, so "the dark-pinned host is less blue than the light-pinned host"
    /// separates the two variants without depending on exact composited
    /// values. While the icon followed `NSApp`, both hosts drew the same
    /// variant and the two measurements landed within noise of each other.
    func testAdapterIconVariantFollowsViewAppearanceNotProcessAppearance() throws {
        let session = try loadAntigravityGhost()

        let darkHosted = blueness(of: host(session, appearance: .darkAqua))
        let lightHosted = blueness(of: host(session, appearance: .aqua))

        // Separation measured on the real renders is ~60; require half of it so
        // the assertion is not tuned to one machine's compositing.
        let margin = 30
        XCTAssertGreaterThan(
            lightHosted - darkHosted, margin,
            """
            Adapter icon did not follow the host view's appearance.
            bluest (B−R) in the icon slot: light-pinned host = \(lightHosted), \
            dark-pinned host = \(darkHosted) (expected light − dark > \(margin)).
            Equal-ish values mean both hosts drew the SAME SVG variant, i.e. the \
            icon resolved its appearance from the process (NSApp) rather than \
            from the view — issue #1509.
            """
        )
    }

    /// The model-level counterpart: the same session yields visibly different
    /// icons for the two appearances. Guards the resolution step itself, so a
    /// regression is attributable without re-reading pixels out of a row.
    func testAdapterIconResolvesADistinctImagePerAppearance() throws {
        let session = try loadAntigravityGhost()
        let dark = try XCTUnwrap(session.adapterIcon(isDark: true))
        let light = try XCTUnwrap(session.adapterIcon(isDark: false))
        XCTAssertNotEqual(
            dark.tiffRepresentation, light.tiffRepresentation,
            "dark and light adapter icons must not be byte-identical"
        )
    }

    /// The bug stated directly: flipping the *process's* appearance must not
    /// change what a view with a pinned appearance renders.
    ///
    /// This is the assertion that would have caught #1509 on the day it
    /// landed. The machine's own appearance is irrelevant to it — it sets
    /// both values itself — so unlike the snapshot references it cannot pass
    /// by day and fail by night.
    func testProcessAppearanceDoesNotChangeTheRenderedIcon() throws {
        // `NSApp` is nil until something instantiates NSApplication, and in
        // this class that otherwise happens only as a side effect of an
        // earlier test's `host()`. Left implicit, this test passes in a full
        // run and fails under `--filter` on its own name — the one gesture
        // someone debugging it would reach for. Create it here so the
        // precondition is self-supplied, then still unwrap: a nil `NSApp`
        // would make the two assignments below no-ops and the assertion
        // vacuous, which must fail loudly rather than read as a pass.
        _ = NSApplication.shared
        let app = try XCTUnwrap(NSApp, "no NSApplication in the test host — this test cannot run")
        let saved = app.appearance
        defer { app.appearance = saved }

        let session = try loadAntigravityGhost()

        app.appearance = NSAppearance(named: .aqua)
        let underLightProcess = blueness(of: host(session, appearance: .darkAqua))

        app.appearance = NSAppearance(named: .darkAqua)
        let underDarkProcess = blueness(of: host(session, appearance: .darkAqua))

        XCTAssertEqual(
            underLightProcess, underDarkProcess,
            """
            The same dark-pinned row rendered differently depending on the \
            PROCESS appearance (light-process = \(underLightProcess), \
            dark-process = \(underDarkProcess)). The adapter icon is reading \
            NSApp instead of the view it is drawn into — issue #1509. On a Mac \
            with auto-appearance this is what makes the ghost-row snapshots \
            flip between red and green at dusk and dawn.
            """
        )
    }

    // MARK: - Helpers

    /// Highest (blue − red) found in the row's trailing icon slot. The slot is
    /// expressed as a fraction of the raster so the probe is independent of the
    /// backing scale factor.
    private func blueness(of view: NSView) -> Int {
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else {
            XCTFail("could not allocate a bitmap rep for \(view)")
            return 0
        }
        view.cacheDisplay(in: view.bounds, to: rep)
        let w = rep.pixelsWide
        let h = rep.pixelsHigh
        // The adapter icon is the right-most element of the row's metrics line.
        let x0 = Int(Double(w) * 0.92)
        let x1 = w - 1
        let y0 = Int(Double(h) * 0.30)
        let y1 = Int(Double(h) * 0.72)
        var best: Int?
        for y in y0...y1 {
            for x in x0...x1 {
                guard let c = rep.colorAt(x: x, y: y) else { continue }
                let r = Int((c.redComponent * 255).rounded())
                let b = Int((c.blueComponent * 255).rounded())
                best = max(best ?? Int.min, b - r)
            }
        }
        // Not `Int.min`: callers subtract two of these, and a sentinel that
        // escapes would trap on overflow — which in Swift aborts the process
        // and would truncate the whole run, the same damage #1523 does.
        guard let measured = best else {
            XCTFail("no pixel in the icon probe box was readable")
            return 0
        }
        return measured
    }

    /// Built through `PinnedSnapshotHost` — the type every other snapshot suite
    /// hosts through — rather than a hand-rolled `NSHostingView`, so this suite
    /// inherits the preference pin (#1662) the way it already inherits the
    /// appearance one, instead of re-implementing it. The appearance is still
    /// this suite's own subject and is passed through.
    private func host(_ session: SessionState, appearance: NSAppearance.Name) -> NSView {
        PinnedSnapshotHost(
            SessionRowView(session: session, agentNumber: 1)
                .environmentObject(sessionManager)
                .frame(width: 350, height: 48)
                .background(Color(NSColor.windowBackgroundColor)),
            width: 350, height: 48, appearance: appearance, defaults: defaults).view
    }

    private func loadAntigravityGhost() throws -> SessionState {
        let dir = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures/SessionRow")
        let data = try Data(contentsOf: dir.appendingPathComponent("antigravity-ghost.json"))
        let decoder = JSONDecoder()
        if let env = try? decoder.decode(Envelope.self, from: data), let s = env.session { return s }
        return try decoder.decode(SessionState.self, from: data)
    }

    private struct Envelope: Decodable {
        let type: String?
        let session: SessionState?
    }
}
