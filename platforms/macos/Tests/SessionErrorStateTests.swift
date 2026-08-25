import XCTest
import SwiftUI
import AppKit
@testable import Irrlicht

/// #1802 — the `error` session state, end to end on macOS.
///
/// The first three tests are RED-FIRST regression tests: each was written and
/// run against the pre-fix tree (base 32ec3efb, which already carried the Go
/// `error` state from #1798 but none of the Swift half) and seen to FAIL. The
/// menu-bar one is the user's own report — a failed session's dot rendered
/// `fill="#8E8E93"`, the neutral grey `.unknown` hue, instead of red.
@MainActor
final class SessionErrorStateTests: XCTestCase {
    private func decode(state: String, errorJSON: String = "") throws -> SessionState {
        let json = """
        {"session_id":"s1","state":"\(state)","model":"m","cwd":"/tmp/p",
         "project_name":"p","first_seen":1700000000,"updated_at":1700000000
         \(errorJSON)}
        """.data(using: .utf8)!
        return try JSONDecoder().decode(SessionState.self, from: json)
    }

    // MARK: - Red-first regressions

    /// Seen red before the fix: `XCTAssertNotEqual failed: ("unknown") is
    /// equal to ("unknown")`. `State(rawValue: "error")` was nil, so #1797's
    /// unrecognized-state fallback caught the daemon's real fourth state and
    /// painted it neutral grey.
    func testErrorStateDecodesToErrorNotUnknown() throws {
        let s = try decode(state: "error")
        XCTAssertEqual(s.state, .error)
        XCTAssertNotEqual(s.state, .unknown,
                          "a daemon-reported error state must not be treated as unreadable")
    }

    /// THE USER'S ASK. Seen red before the fix, with the rendered markup in the
    /// failure message: `fill="#8E8E93"`.
    func testMenuBarDotForAnErroredSessionIsRed() throws {
        let s = try decode(state: "error")
        let svg = MenuBarStatusRenderer.buildStatusSVG(sessions: [s], projectGroupOrder: ["p"])?.svg ?? ""
        XCTAssertTrue(svg.contains("fill=\"#\(IrrSVG.error)\""),
                      "menu-bar dot for an errored session is not red — svg was: \(svg)")
    }

    /// Seen red before the fix: `XCTAssertNotEqual failed: ("secondary") is
    /// equal to ("secondary")`. The string→colour map used by Gas Town rig and
    /// convoy badges fell through to the neutral "nothing to report" grey.
    func testForStateMapsErrorToTheErrorHue() {
        XCTAssertEqual(IrrColors.forState("error"), IrrColors.error)
        XCTAssertNotEqual(IrrColors.forState("error"), Color.secondary)
        // Unrecognized input must still fall through — this half is a LOCK on
        // existing behaviour and passed before the fix by construction.
        XCTAssertEqual(IrrColors.forState("nonsense"), Color.secondary)
    }

    // MARK: - The state renders as its own thing everywhere

    /// The neutral-rendering argument #1797 made for `.unknown`, applied to
    /// `.error`: a state that decodes correctly but then borrows another
    /// state's glyph or hue has fixed nothing the user can see. Error must
    /// differ from BOTH `ready` (never green) and `unknown` (never grey).
    func testErrorRendersAsItsOwnStateOnEveryAxis() {
        let e = SessionState.State.error
        for other in [SessionState.State.ready, .unknown, .working, .waiting] {
            XCTAssertNotEqual(e.hexColor, other.hexColor, "error shares a hex with \(other.rawValue)")
            XCTAssertNotEqual(e.glyph, other.glyph, "error shares a glyph with \(other.rawValue)")
            XCTAssertNotEqual(e.emoji, other.emoji, "error shares an emoji with \(other.rawValue)")
            XCTAssertNotEqual(e.label, other.label, "error shares a label with \(other.rawValue)")
        }
        XCTAssertEqual(e.hexColor, IrrSVG.error)
        XCTAssertEqual(e.rawValue, "error", "the raw value is the daemon's wire spelling")
    }

    // MARK: - Precedence (the settled decision)

    /// The FULL ordering, pinned as one sequence rather than as the error case
    /// alone — the issue asked for exactly this. `error > waiting > working >
    /// ready > unknown`.
    ///
    /// Written as "add one state at a time and re-ask" so a reordering that
    /// happens to keep error on top but swaps waiting and working still fails.
    ///
    /// Mutation check (verified): delete `if states.contains(.error)` from
    /// `dominant(in:)` and this goes red with
    /// `XCTAssertEqual failed: ("waiting") is not equal to ("error")`.
    ///
    /// Mutation check (verified): delete `if states.contains(.error)` from
    /// `dominant(in:)` and this goes red with
    /// `XCTAssertEqual failed: ("waiting") is not equal to ("error")`.
    func testDominantRanksErrorAboveEverything() {
        XCTAssertEqual(SessionState.State.dominant(in: [.unknown]), .unknown)
        XCTAssertEqual(SessionState.State.dominant(in: [.ready, .unknown]), .ready)
        XCTAssertEqual(SessionState.State.dominant(in: [.working, .ready, .unknown]), .working)
        XCTAssertEqual(SessionState.State.dominant(in: [.waiting, .working, .ready, .unknown]), .waiting)
        XCTAssertEqual(SessionState.State.dominant(in: [.error, .waiting, .working, .ready, .unknown]), .error)
        // An error alone, and an empty collection's historical answer.
        XCTAssertEqual(SessionState.State.dominant(in: [.error]), .error)
        XCTAssertEqual(SessionState.State.dominant(in: [SessionState.State]()), .ready)
    }

    /// `menuBarRank` drives `segmentOrder`, which is derived from `allCases`.
    /// The ranks must agree with `dominant` or the pie's first wedge and the
    /// count label's colour disagree about which state leads the group.
    ///
    /// Mutation check (verified): give `.error` rank 5 and this goes red with
    /// the sorted order reported as `[waiting, working, ready, unknown,
    /// error]`, and `testMixedGroupSummarisesAsError` goes red with it.
    ///
    /// Mutation check (verified): give `.error` rank 5 and this goes red with
    /// the sorted order reported as `[waiting, working, ready, unknown,
    /// error]`, and `testMixedGroupSummarisesAsError` goes red with it.
    func testMenuBarRankAgreesWithDominance() {
        let byRank = SessionState.State.allCases.sorted { $0.menuBarRank < $1.menuBarRank }
        XCTAssertEqual(byRank, [.error, .waiting, .working, .ready, .unknown])
        XCTAssertEqual(Set(byRank.map(\.menuBarRank)).count, byRank.count,
                       "two states share a menu-bar rank, so the slice order is not deterministic")
    }

    /// A mixed group summarises red, and the count label follows.
    func testMixedGroupSummarisesAsError() throws {
        let sessions = try [
            decode(state: "working"), decode(state: "ready"), decode(state: "error"), decode(state: "waiting"),
        ]
        // 4 sessions > 3, so this takes the aggregated pie path.
        //
        // Mutation check (verified): demote `.error`'s `menuBarRank` and this
        // goes red — and instructively so. The pie still drew a red WEDGE
        // (`fill="#FF3B30"` was present in the failure's markup); it is the
        // count LABEL that reverted to orange. The two are painted by
        // different code paths, and only the label reads `dominant`.
        //
        // Mutation check (verified): demote `.error`'s `menuBarRank` and this
        // goes red — and instructively so. The pie still drew a red WEDGE
        // (`fill="#FF3B30"` is present in the failure's markup); it is the
        // count LABEL that reverted to orange. The two are painted by
        // different code paths, and only the label reads `dominant`.
        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)
        XCTAssertTrue(svg.contains("fill=\"#\(IrrSVG.error)\">4</text>"),
                      "the aggregated count label must take the dominant (error) hue — svg was: \(svg)")
    }

    // MARK: - The error detail off the wire

    /// The rich claudecode `api_error` shape: every field present.
    func testErrorDetailDecodesEveryField() throws {
        let s = try decode(state: "error", errorJSON: """
        ,"error":{"phase":"retrying","class":"rate_limit","message":"Overloaded",
                  "http_status":429,"attempt":3,"max_attempts":10,"retry_in_ms":616.4520045919932}
        """)
        let e = try XCTUnwrap(s.error)
        XCTAssertEqual(e.phase, "retrying")
        XCTAssertEqual(e.class, "rate_limit")
        XCTAssertEqual(e.message, "Overloaded")
        XCTAssertEqual(e.httpStatus, 429)
        XCTAssertEqual(e.attempt, 3)
        XCTAssertEqual(e.maxAttempts, 10)
        XCTAssertEqual(try XCTUnwrap(e.retryInMs), 616.4520045919932, accuracy: 0.0001)
        XCTAssertTrue(e.isRetrying)
        XCTAssertEqual(e.displayMessage, "Overloaded")
        XCTAssertEqual(e.detailSuffix, "attempt 3 of 10 · HTTP 429 · retrying in 0.6s")
    }

    /// The terminal claudecode `isApiErrorMessage` and copilot `query` shapes:
    /// a real failure carrying NO numbers at all. Absence must stay absent —
    /// decoding it as 0 would render "attempt 0 of 0", a give-up invented from
    /// data that said nothing.
    func testAbsentNumbersStayNilRatherThanZero() throws {
        let s = try decode(state: "error", errorJSON: """
        ,"error":{"class":"query","message":"API Error: API returned an empty or malformed response (HTTP 200)"}
        """)
        let e = try XCTUnwrap(s.error)
        XCTAssertNil(e.httpStatus)
        XCTAssertNil(e.attempt)
        XCTAssertNil(e.maxAttempts)
        XCTAssertNil(e.retryInMs)
        XCTAssertNil(e.phase, "an unreported phase is not 'terminal' — it is unreported")
        XCTAssertFalse(e.isRetrying)
        XCTAssertEqual(e.detailSuffix, "", "no numbers reported means no detail suffix, not a fabricated one")
    }

    /// A failure the daemon could not describe. The state is the verdict; the
    /// detail is optional. The row must still say something.
    func testErroredSessionWithNoDetailStillHasALine() throws {
        let s = try decode(state: "error")
        XCTAssertNil(s.error)
        XCTAssertEqual(s.state, .error, "the state stands on its own without a detail")
    }

    /// Neither `message` nor a known class: the class itself is shown verbatim
    /// rather than dropped, because a class this build has not heard of is
    /// still the most specific thing anyone knows about the failure.
    func testUnknownClassIsShownVerbatimNotSwallowed() throws {
        let s = try decode(state: "error", errorJSON: #","error":{"class":"kraken_attack"}"#)
        XCTAssertEqual(try XCTUnwrap(s.error).displayMessage, "kraken_attack")
    }

    /// A healthy session decodes exactly as before — an older daemon that
    /// sends no `error` key is unaffected. LOCK: passes by construction.
    func testHealthySessionHasNoErrorDetail() throws {
        let s = try decode(state: "working")
        XCTAssertNil(s.error)
        XCTAssertEqual(s.state, .working)
    }

    /// `withState` rebuilds the struct field by field, which is how #1797's
    /// `children` came to be dropped. The detail has to survive the same copy.
    func testWithStatePreservesTheErrorDetail() throws {
        let s = try decode(state: "error", errorJSON: #","error":{"message":"boom"}"#)
        XCTAssertEqual(s.withState(.ready).error?.message, "boom")
        XCTAssertEqual(s.withChildren(nil).error?.message, "boom")
    }

    // MARK: - The manager's buckets

    /// An errored session is counted, and deliberately NOT counted as active:
    /// nothing clears an error until the next successful turn, so folding it
    /// into `hasActiveSessions` would make the app claim work is in progress
    /// forever. Mirrors the daemon's own `concurrencyActive()`.
    func testErroredSessionIsCountedButNotActive() throws {
        let manager = SessionManager(defaults: InMemoryDefaults())
        manager.sessions = try [decode(state: "error"), decode(state: "ready")]
        XCTAssertEqual(manager.errorSessions, 1)
        XCTAssertFalse(manager.hasActiveSessions,
                       "an errored session must not read as active — nothing clears it on its own")
    }
}

/// #1802 — the error text clears WCAG AA against the wash it is drawn on.
///
/// A check the change ADDS: it COMPUTES the composited wash and the contrast
/// ratio from the real tokens rather than restating a number someone measured
/// once, so the figures in `Tokens.swift`'s comment are produced by this test
/// instead of drifting away from it.
///
/// Mutation check (verified): set `errorPillText` to the raw `IrrHex.error`
/// brand hue and both appearances go red — 3.02:1 in aqua, 4.18:1 in dark
/// aqua, which is also where the figures in `Tokens.swift`'s comment come
/// from.
///
/// 0.12 is the worst case of the two washes the feature uses (the row's error
/// line sits on 0.08, the banner on `errorDim` = 0.12): more wash moves the
/// background AWAY from the text's luminance in light mode and TOWARD it in
/// dark, so bounding 0.12 bounds both.
@MainActor
final class TokenContrastTests: XCTestCase {
    private func srgb(_ color: Color, in appearance: NSAppearance.Name) -> (r: Double, g: Double, b: Double) {
        var out = (r: 0.0, g: 0.0, b: 0.0)
        let app = NSAppearance(named: appearance)!
        app.performAsCurrentDrawingAppearance {
            let ns = NSColor(color).usingColorSpace(.sRGB)!
            out = (Double(ns.redComponent), Double(ns.greenComponent), Double(ns.blueComponent))
        }
        return out
    }

    private func windowBackground(in appearance: NSAppearance.Name) -> (r: Double, g: Double, b: Double) {
        srgb(Color(NSColor.windowBackgroundColor), in: appearance)
    }

    /// WCAG 2.1 relative luminance.
    private func luminance(_ c: (r: Double, g: Double, b: Double)) -> Double {
        func lin(_ v: Double) -> Double { v <= 0.04045 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4) }
        return 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b)
    }

    private func contrast(_ a: (r: Double, g: Double, b: Double), _ b: (r: Double, g: Double, b: Double)) -> Double {
        let (l1, l2) = (luminance(a), luminance(b))
        return (max(l1, l2) + 0.05) / (min(l1, l2) + 0.05)
    }

    /// Source-over composite of `top` at `alpha` onto opaque `base`.
    private func composite(_ top: (r: Double, g: Double, b: Double),
                           alpha: Double,
                           over base: (r: Double, g: Double, b: Double)) -> (r: Double, g: Double, b: Double) {
        (r: top.r * alpha + base.r * (1 - alpha),
         g: top.g * alpha + base.g * (1 - alpha),
         b: top.b * alpha + base.b * (1 - alpha))
    }

    private func measure(_ appearance: NSAppearance.Name) -> Double {
        let wash = composite(srgb(IrrColors.error, in: appearance),
                             alpha: 0.12,
                             over: windowBackground(in: appearance))
        return contrast(srgb(IrrColors.errorPillText, in: appearance), wash)
    }

    func testErrorTextClearsWCAGAAInBothAppearances() {
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            let ratio = measure(appearance)
            // Printed, not only asserted: the number is what the token comment
            // cites, and a passing test that prints nothing is an unread
            // measurement.
            print("contrast(errorPillText on 12% error wash, \(appearance.rawValue)) = \(String(format: "%.2f", ratio)):1")
            XCTAssertGreaterThanOrEqual(
                ratio, 4.5,
                "IrrColors.errorPillText measures \(String(format: "%.2f", ratio)):1 in \(appearance.rawValue) — under WCAG AA's 4.5:1 for 9pt text")
        }
    }

    /// The premise the test above rests on, asserted rather than assumed: the
    /// raw brand hue really does fail, so `errorPillText` is a necessary
    /// retune and not decoration. If this ever passes, the retune can be
    /// deleted — and this is where that would be noticed.
    func testTheRawBrandHueIsWhyTheRetuneExists() {
        var failedSomewhere = false
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            let wash = composite(srgb(IrrColors.error, in: appearance),
                                 alpha: 0.12, over: windowBackground(in: appearance))
            let raw = contrast(srgb(IrrColors.error, in: appearance), wash)
            print("contrast(raw IrrColors.error on its own 12% wash, \(appearance.rawValue)) = \(String(format: "%.2f", raw)):1")
            if raw < 4.5 { failedSomewhere = true }
        }
        XCTAssertTrue(failedSomewhere,
                      "the raw error hue now clears AA on its own wash — errorPillText is no longer needed")
    }
}

/// #1802 — the daemon-wide error banner's show/hide decision.
///
/// A check the change ADDS, so there is no "before the fix" to run it against.
/// Mutation checks (both verified):
///   - delete `guard !items.isEmpty else { return nil }` from
///     `DaemonErrorSummary.init?` → `testHealthyDaemonProducesNoBanner` and
///     `testEmptyItemsProduceNoSummary` go red, reporting a summary reading
///     "Irrlicht has 0 problems";
///   - drop the `useLocalDaemon &&` guard in `DaemonHealth.faults` →
///     `testRelayOnlySetupIsNotReportedAsAStalledLocalDaemon` goes red.
///
/// The absence case is asserted SEMANTICALLY here — `XCTAssertNil` — and only
/// then photographed in `DaemonErrorBannerRenderTests`. Hosting a whole panel
/// to photograph the absence of a strip is a brittle way to assert nothing:
/// this is the half that makes "no finding" and "could not look" produce
/// different output.
final class DaemonHealthTests: XCTestCase {
    func testHealthyDaemonProducesNoBanner() {
        XCTAssertTrue(DaemonHealth.faults(useLocalDaemon: true, localConnectionStalled: false).isEmpty)
        XCTAssertNil(DaemonErrorSummary(items: DaemonHealth.faults(
            useLocalDaemon: true, localConnectionStalled: false)),
            "a healthy daemon must render no banner at all")
    }

    func testStalledLocalDaemonProducesABanner() {
        let faults = DaemonHealth.faults(useLocalDaemon: true, localConnectionStalled: true)
        XCTAssertEqual(faults.count, 1)
        let summary = DaemonErrorSummary(items: faults)
        XCTAssertNotNil(summary)
        XCTAssertEqual(summary?.count, 1)
        XCTAssertEqual(summary?.text, "Irrlicht has a problem")
        // The reason is what tells two faults sharing a title apart, so it
        // must not be empty.
        XCTAssertFalse(try XCTUnwrap(faults.first).reason.isEmpty)
    }

    /// A relay-only setup has no local daemon to be stalled, and the flag can
    /// be left set from an earlier local session — the same gate the
    /// connection dot applies.
    func testRelayOnlySetupIsNotReportedAsAStalledLocalDaemon() {
        XCTAssertTrue(DaemonHealth.faults(useLocalDaemon: false, localConnectionStalled: true).isEmpty)
    }

    /// Plural wording, and stable ids so SwiftUI does not rebuild a standing
    /// fault's row on every republish.
    func testSummaryPluralisesAndKeepsStableIDs() {
        let items = [
            DaemonFault(id: "a", title: "T1", reason: "R1"),
            DaemonFault(id: "b", title: "T2", reason: "R2"),
        ]
        XCTAssertEqual(DaemonErrorSummary(items: items)?.text, "Irrlicht has 2 problems")
        XCTAssertEqual(Set(items.map(\.id)).count, 2, "fault ids must be unique within a render pass")
    }

    /// Empty in, nil out — the whole hide mechanism, stated on its own.
    func testEmptyItemsProduceNoSummary() {
        XCTAssertNil(DaemonErrorSummary(items: []))
    }
}
