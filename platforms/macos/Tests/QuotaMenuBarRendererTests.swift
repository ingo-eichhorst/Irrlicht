import XCTest
@testable import Irrlicht

/// Coverage for the menu-bar quota icon (issue #909): the stacked 5h/7d
/// bars, the single-window compact ring, the pace marker both share, the
/// pace-aware color ramp (must mirror SessionListView.barColor exactly —
/// see QuotaChipBarColorTests for the popover-side pin of the same table),
/// and the freshest-renderable snapshot selection across live sessions.
///
/// ## What #1675 changed here, and why it matters more in this suite than most
///
/// Every fixture below used to be built from `Date()` and every renderer entry
/// point read `Date()` again a moment later, so the pace this suite *asked for*
/// and the pace the renderer *computed* were two reads of the machine that
/// merely agreed to within a millisecond. Two consequences, both now gone:
/// the suite could not pin an exact ramp boundary (its own comment on
/// `testBuildSVGColorThresholdsMirrorPopoverPaceRamp` recorded that as a
/// standing limitation), and nothing in it could tell a renderer that honoured
/// the instant it was given from one that ignored the argument and read the
/// clock — the two answer identically whenever the argument IS the clock.
///
/// `now` is now a fixed instant and a required argument, so the fixtures are
/// absolute, the boundary rows are exact, and
/// `testTheBarsHonourTheNowTheyAreGiven` / `…TheCircle…` drive two clocks
/// through one snapshot and refuse an unmoved marker. Unlike most of this
/// family, this suite takes no image snapshot, so `ImageSnapshotCIScopeTests`
/// does not skip it and all of the above is graded on a CI runner.
@MainActor
final class QuotaMenuBarRendererTests: XCTestCase {

    /// The instant every fixture in this suite is built relative to, and the
    /// one every renderer call is given. `PinnedNowSnapshot.referenceNow`
    /// rather than a local constant so this suite and the popover-side
    /// `QuotaChipClockTests` place a window at the same place on the timeline.
    private let now = PinnedNowSnapshot.referenceNow

    /// A different day in every time zone (`referenceNow + 48h`), so an arm
    /// that drives two clocks cannot be made tautological by moving them
    /// closer together.
    private let laterNow = PinnedNowSnapshot.contrastingNow

    // MARK: - buildSVG (bars)

    func testBuildSVGReturnsNilWhenNoWindows() {
        let info = RateLimitInfo(windows: [], sampledAt: now)
        XCTAssertNil(QuotaMenuBarRenderer.buildSVG(for: info, now: now))
    }

    func testBuildSVGIncludesBothRowsWhenBothWindowsPresent() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains(">5h<"))
        XCTAssertTrue(result!.svg.contains(">7d<"))
    }

    func testBuildSVGOmitsMissingWindowRow() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 40, windowMinutes: 10080, resetsAt: now.addingTimeInterval(3600))],
            sampledAt: now
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        XCTAssertNotNil(result)
        XCTAssertFalse(result!.svg.contains(">5h<"))
        XCTAssertTrue(result!.svg.contains(">7d<"))
    }

    // MARK: - buildSVG compact mode (Combined style: no labels, narrower bars)

    func testBuildSVGCompactOmitsWindowLabels() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let result = QuotaMenuBarRenderer.buildSVG(for: info, compact: true, now: now)
        XCTAssertNotNil(result)
        XCTAssertFalse(result!.svg.contains(">5h<"))
        XCTAssertFalse(result!.svg.contains(">7d<"))
    }

    func testBuildSVGCompactIsNarrowerThanDefault() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let normal = QuotaMenuBarRenderer.buildSVG(for: info, compact: false, now: now)
        let compact = QuotaMenuBarRenderer.buildSVG(for: info, compact: true, now: now)
        XCTAssertNotNil(normal)
        XCTAssertNotNil(compact)
        XCTAssertLessThan(compact!.width, normal!.width)
        // compact's total width IS the bar width (no label/gap column), so
        // it can be compared directly against the known 32pt default bar
        // width: the requested range is 30-40% narrower, i.e. 60-70% of 32.
        XCTAssertGreaterThanOrEqual(compact!.width, 32 * 0.60)
        XCTAssertLessThanOrEqual(compact!.width, 32 * 0.70)
    }

    func testBuildSVGCompactDefaultsToFalseWhenOmitted() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let omitted = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        let explicitFalse = QuotaMenuBarRenderer.buildSVG(for: info, compact: false, now: now)
        XCTAssertEqual(omitted?.width, explicitFalse?.width)
        XCTAssertTrue(omitted!.svg.contains(">5h<"))
    }

    /// Same ramp QuotaChipBarColorTests pins for SessionListView.barColor
    /// — this renderer must reach the identical verdict (as a bare hex
    /// instead of a SwiftUI Color) so the same window can't read green in
    /// the icon while the popover shows it orange.
    ///
    /// The **exact** boundary rows (delta precisely 5 and precisely 15) are
    /// #1675's. Before it, `windowWithPace` derived `resetsAt` from one
    /// `Date()` and `buildSVG` re-derived the pace from another a moment
    /// later, so a delta sitting exactly on a threshold could tip either way
    /// on sub-millisecond drift and this suite had to stay off the boundary
    /// on purpose. With `now` an input on both sides the arithmetic is exact:
    /// `windowWithPace(pace: 30)` places `resetsAt` so that
    /// `quotaPacePercent(…, now: now)` returns 30.0 and nothing else.
    func testBuildSVGColorThresholdsMirrorPopoverPaceRamp() {
        let cases: [(name: String, used: Double, pace: Double, wantHex: String)] = [
            ("on pace",                30, 30, "34C759"),
            ("clearly still green",    33, 30, "34C759"),
            ("exactly on the yellow boundary (delta 5)",  35, 30, "FFCC00"),
            ("one point below it (delta 4.9)",          34.9, 30, "34C759"),
            ("clearly yellow",         36, 30, "FFCC00"),
            ("exactly on the orange boundary (delta 15)", 45, 30, "FF9500"),
            ("one point below it (delta 14.9)",         44.9, 30, "FFCC00"),
            ("clearly orange",         46, 30, "FF9500"),
            ("far ahead",              70, 30, "FF9500"),
            ("behind pace",            20, 40, "34C759"),
            ("at cap (85%) overrides pace", 85, 50, "FF9500"),
        ]
        for c in cases {
            let window = windowWithPace(usedPercent: c.used, pace: c.pace)
            let info = RateLimitInfo(windows: [window], sampledAt: now)
            let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
            XCTAssertTrue(
                result?.svg.contains("#\(c.wantHex)") ?? false,
                "\(c.name): expected #\(c.wantHex) in \(result?.svg ?? "nil")"
            )
        }
    }

    /// pace nil (no resetsAt) falls back to the absolute-only ramp, same as
    /// SessionListView.barColor's nil-pace branch.
    func testBuildSVGColorThresholdsFallBackToAbsoluteRampWhenUnpaceable() {
        let cases: [(name: String, used: Double, wantHex: String)] = [
            ("nil pace, low usage",    30, "34C759"),
            ("nil pace, 50% — yellow", 50, "FFCC00"),
            ("nil pace, 70% — orange", 70, "FF9500"),
        ]
        for c in cases {
            let info = RateLimitInfo(
                windows: [RateLimitWindowInfo(usedPercent: c.used, windowMinutes: 300, resetsAt: Date(timeIntervalSince1970: 0))],
                sampledAt: now
            )
            let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
            XCTAssertTrue(
                result?.svg.contains("#\(c.wantHex)") ?? false,
                "\(c.name): expected #\(c.wantHex) in \(result?.svg ?? "nil")"
            )
        }
    }

    // MARK: - buildCircleSVG

    func testBuildCircleSVGReturnsNilWhenNoWindows() {
        let info = RateLimitInfo(windows: [], sampledAt: now)
        XCTAssertNil(QuotaMenuBarRenderer.buildCircleSVG(for: info, now: now))
    }

    /// Regression pin: the circle must stay pinned to the 5h window even
    /// when the 7d window is more depleted — RateLimitInfo.imminentWindow
    /// would pick 7d here (80 > 20), which is deliberately *not* what the
    /// circle shows. A glance-value shouldn't silently swap which window
    /// it's reading. Colors chosen so a wrong-window regression is
    /// unambiguous: the 5h reading (20% used, 80% pace — well behind pace)
    /// is green, while the 7d reading (80% used, ~57% pace — ahead of pace)
    /// would be orange.
    func testBuildCircleSVGPrefersFiveHourOverSevenDayEvenWhenLessDepleted() {
        let info = makeInfo(fiveHour: 20, sevenDay: 80, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info, now: now)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains("#34C759"), "should use the 5h window's green (behind pace)")
        XCTAssertFalse(result!.svg.contains("#FF9500"), "must not leak the 7d window's orange (ahead of pace)")
    }

    func testBuildCircleSVGFallsBackToSevenDayWhenFiveHourAbsent() {
        // Fresh 7d window (pace ≈ 0) so 60% used is unambiguously ahead of
        // pace → orange, isolating "did it use the 7d window at all" from
        // the color-ramp tests above.
        let window = windowWithPace(usedPercent: 60, pace: 0, windowMinutes: 10080)
        let info = RateLimitInfo(windows: [window], sampledAt: now)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info, now: now)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains("#FF9500"))
    }

    // MARK: - pace marker (mirrors SessionListView.quotaPacePercent)

    func testPaceMarkerRenderedOnBarsWhenWindowHasFutureReset() {
        let info = makeInfo(fiveHour: 20, sevenDay: nil, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        XCTAssertTrue(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerAbsentOnBarsWhenResetIsUnset() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 20, windowMinutes: 300, resetsAt: Date(timeIntervalSince1970: 0))],
            sampledAt: now
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        XCTAssertFalse(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerStillRendersClampedWhenWindowAlreadyExpired() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 20, windowMinutes: 300, resetsAt: now.addingTimeInterval(-60))],
            sampledAt: now
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info, now: now)
        XCTAssertTrue(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerRenderedOnCircleAsRedStroke() {
        let info = makeInfo(fiveHour: 20, sevenDay: nil, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info, now: now)
        XCTAssertTrue(result!.svg.contains("stroke=\"red\""))
    }

    // MARK: - …and the marker is placed by the `now` the renderer is GIVEN (#1675)

    /// The load-bearing arm of #1675 on this side: one snapshot, two clocks,
    /// and the pace marker must move. A renderer that ignores its `now:`
    /// argument and reads `Date()` — which is exactly what every line here did
    /// before #1675 — answers the same x twice and fails only here.
    ///
    /// The 7d window is present so the assertion covers both `rowSVG` calls:
    /// each used to read the clock for itself, so one icon could pace its two
    /// rows against two different instants.
    func testTheBarsHonourTheNowTheyAreGiven() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)

        // The premise, loudly first: 48 hours must actually change the pace for
        // this fixture, or the comparison below measures nothing.
        let paceAtNow = SessionListView.quotaPacePercent(info.windows[0], now: now)
        let paceLater = SessionListView.quotaPacePercent(info.windows[0], now: laterNow)
        XCTAssertNotEqual(paceAtNow, paceLater,
                          "the fixture's pace is the same under both clocks — this arm cannot "
                          + "fail for the right reason")

        guard let early = QuotaMenuBarRenderer.buildSVG(for: info, now: now),
              let late = QuotaMenuBarRenderer.buildSVG(for: info, now: laterNow) else {
            return XCTFail("the bars did not render at all — this check cannot have run")
        }
        // "no marker" and "an unmoved marker" must not produce the same verdict.
        let earlyMarkers = Self.markerXs(in: early.svg, of: .barRect)
        let lateMarkers = Self.markerXs(in: late.svg, of: .barRect)
        XCTAssertEqual(earlyMarkers.count, 2,
                       "expected one pace marker per row in \(early.svg)")
        XCTAssertEqual(lateMarkers.count, 2,
                       "expected one pace marker per row in \(late.svg)")
        XCTAssertNotEqual(earlyMarkers, lateMarkers,
                          "the pace markers sit at the same x under two clocks 48h apart — "
                          + "`now:` is reaching nothing and the position is coming from the "
                          + "machine's wall clock")
    }

    /// …and the ring's marker, which is placed by different arithmetic
    /// (`buildCircleSVG` converts the pace to an angle) and so is not covered
    /// by the arm above.
    func testTheCircleHonoursTheNowItIsGiven() {
        let info = makeInfo(fiveHour: 20, sevenDay: nil, fiveHourResetsIn: 3600)
        XCTAssertNotEqual(SessionListView.quotaPacePercent(info.windows[0], now: now),
                          SessionListView.quotaPacePercent(info.windows[0], now: laterNow),
                          "the fixture's pace is the same under both clocks")

        guard let early = QuotaMenuBarRenderer.buildCircleSVG(for: info, now: now),
              let late = QuotaMenuBarRenderer.buildCircleSVG(for: info, now: laterNow) else {
            return XCTFail("the ring did not render at all — this check cannot have run")
        }
        let earlyLine = Self.markerXs(in: early.svg, of: .ringTick)
        let lateLine = Self.markerXs(in: late.svg, of: .ringTick)
        XCTAssertEqual(earlyLine.count, 1, "expected one pace line in \(early.svg)")
        XCTAssertEqual(lateLine.count, 1, "expected one pace line in \(late.svg)")
        XCTAssertNotEqual(earlyLine, lateLine,
                          "the ring's pace line sits at the same x under two clocks 48h apart — "
                          + "`now:` is reaching nothing")
    }

    /// The two shapes the pace marker is drawn as, each naming the element it
    /// is and the coordinate that positions it.
    ///
    /// An enum rather than two loose `String` parameters so the wrong pairing
    /// (`x` with the ring's `stroke="red"`, which matches nothing and would
    /// report "no marker") is not expressible — and so a reader sees which
    /// element each arm is looking for.
    private enum PaceMarkerShape {
        /// `<rect … fill="red"/>`, one per bar row.
        case barRect
        /// `<line … stroke="red"/>`, the ring's radial tick.
        case ringTick

        /// The attribute carrying the marker's horizontal position.
        var positionAttribute: String {
            switch self {
            case .barRect: return "x"
            case .ringTick: return "x1"
            }
        }

        /// A fragment appearing only in this element.
        var signature: String {
            switch self {
            case .barRect: return "fill=\"red\""
            case .ringTick: return "stroke=\"red\""
            }
        }
    }

    /// Every marker of `shape` in `svg`, by its horizontal position.
    ///
    /// Deliberately a text scan of the emitted SVG rather than a re-computation
    /// of the geometry: re-deriving the expected x from `quotaPacePercent` would
    /// pass for a renderer that computes the right number and then draws
    /// somewhere else.
    private static func markerXs(in svg: String, of shape: PaceMarkerShape) -> [String] {
        svg.components(separatedBy: "<").compactMap { element -> String? in
            guard element.contains(shape.signature) else { return nil }
            guard let start = element.range(of: "\(shape.positionAttribute)=\"") else { return nil }
            let rest = element[start.upperBound...]
            guard let end = rest.range(of: "\"") else { return nil }
            return String(rest[..<end.lowerBound])
        }
    }

    // MARK: - selectedSnapshot

    func testSelectedSnapshotPicksFreshestAcrossSessions() {
        let older = makeSession(id: "1", adapter: "claude-code", usedPercent: 10, sampledSecondsAgo: 120)
        let newer = makeSession(id: "2", adapter: "claude-code", usedPercent: 90, sampledSecondsAgo: 5)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [older, newer], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 90)
    }

    func testSelectedSnapshotFiltersByProviderKey() {
        let claude = makeSession(id: "1", adapter: "claude-code", usedPercent: 10, sampledSecondsAgo: 5)
        let codex = makeSession(id: "2", adapter: "codex", usedPercent: 90, sampledSecondsAgo: 5)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [claude, codex], providerKey: "anthropic")
        XCTAssertEqual(got?.windows.first?.usedPercent, 10)
    }

    /// The popover (SessionListView.mergeIntoBuckets) keeps a stale
    /// snapshot and dims the chip rather than dropping it — the icon has
    /// no room to dim, but dropping entirely made an active session look
    /// idle (it collapsed the whole quota display, which for `.usage`
    /// style meant falling back to the "no sessions" icon). Keeping it
    /// matches the popover's intent: show the last-known reading until the
    /// next statusline tick refreshes it.
    func testSelectedSnapshotKeepsStaleSnapshotsRatherThanDroppingThem() {
        let staleRateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 10, windowMinutes: 300, resetsAt: now.addingTimeInterval(-3600))],
            sampledAt: now.addingTimeInterval(-5)
        )
        let stale = sessionState(id: "1", adapter: "claude-code", rateLimit: staleRateLimit)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [stale], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 10)
    }

    /// A snapshot with no windows (the credits/usage-only path) can never
    /// render — it must not win the freshest-wins race over an older
    /// snapshot that actually has data.
    func testSelectedSnapshotSkipsSnapshotWithEmptyWindows() {
        let unrenderable = sessionState(
            id: "unrenderable", adapter: "claude-code",
            rateLimit: RateLimitInfo(windows: [], sampledAt: now) // freshest, but empty
        )
        let renderable = makeSession(id: "2", adapter: "claude-code", usedPercent: 42, sampledSecondsAgo: 120)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [unrenderable, renderable], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 42)
    }

    func testSelectedSnapshotReturnsNilWhenNoSessionCarriesRateLimit() {
        let plain = SessionState(
            id: "sess_1", state: .working, model: "claude-sonnet", cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: now, updatedAt: now
        )
        XCTAssertNil(QuotaMenuBarRenderer.selectedSnapshot(sessions: [plain], providerKey: nil))
    }

    // MARK: - helpers

    private func makeInfo(fiveHour: Double?, sevenDay: Double?, fiveHourResetsIn: TimeInterval = 3600) -> RateLimitInfo {
        var windows: [RateLimitWindowInfo] = []
        if let fiveHour {
            windows.append(RateLimitWindowInfo(usedPercent: fiveHour, windowMinutes: 300, resetsAt: now.addingTimeInterval(fiveHourResetsIn)))
        }
        if let sevenDay {
            windows.append(RateLimitWindowInfo(usedPercent: sevenDay, windowMinutes: 10080, resetsAt: now.addingTimeInterval(3 * 86400)))
        }
        return RateLimitInfo(windows: windows, sampledAt: now)
    }

    /// Builds a window whose `resetsAt` implies exactly `pace` percent
    /// elapsed, so color-ramp tests can target specific (used, pace) pairs
    /// instead of whatever a fixed resetsAt happens to imply.
    private func windowWithPace(usedPercent: Double, pace: Double, windowMinutes: Int = 300) -> RateLimitWindowInfo {
        let windowSeconds = Double(windowMinutes) * 60
        let elapsed = (pace / 100) * windowSeconds
        let resetsAt = now.addingTimeInterval(windowSeconds - elapsed)
        return RateLimitWindowInfo(usedPercent: usedPercent, windowMinutes: windowMinutes, resetsAt: resetsAt)
    }

    /// Common case: a session with a fresh (future-resetting) rate-limit
    /// window. Tests that need an already-expired window build a
    /// RateLimitInfo directly and go through `sessionState` instead, so
    /// this stays at 4 arguments rather than growing a resetsInPast flag
    /// nobody but one test needed (CodeScene: excess function arguments).
    private func makeSession(
        id: String,
        adapter: String,
        usedPercent: Double,
        sampledSecondsAgo: TimeInterval
    ) -> SessionState {
        let rateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: usedPercent, windowMinutes: 300, resetsAt: now.addingTimeInterval(3600))],
            sampledAt: now.addingTimeInterval(-sampledSecondsAgo)
        )
        return sessionState(id: id, adapter: adapter, rateLimit: rateLimit)
    }

    private func sessionState(id: String, adapter: String, rateLimit: RateLimitInfo) -> SessionState {
        let metrics = SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: 0,
            modelName: "claude-sonnet",
            contextWindow: nil,
            contextUtilization: 0,
            pressureLevel: "safe",
            contextWindowUnknown: nil,
            estimatedCostUSD: nil,
            lastAssistantText: nil,
            tasks: nil,
            rateLimit: rateLimit
        )
        return SessionState(
            id: "sess_\(id)",
            state: .working,
            model: "claude-sonnet",
            cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: now,
            updatedAt: now,
            metrics: metrics,
            adapter: adapter
        )
    }
}
