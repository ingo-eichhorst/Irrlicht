import AppKit
import Foundation

struct MenuBarStatusRenderer {
    struct StateSegment: Equatable {
        let state: SessionState.State
        let count: Int
        let fraction: Double
    }

    private struct GroupRender {
        let elements: String
        let width: CGFloat
    }

    // radius / overlap / groupGap / countDigitWidth / overflowWidth are
    // non-private so a test can state the icon's measured width budget by
    // READING them rather than restating their values, which drift silently —
    // the idiom `QuotaMenuBarRenderer.labelWidth`/`barWidth`/`gap` already
    // follows for the quota half (#1845), extended to the dot half by #1955's
    // user-settable slot budget. `MenuBarAppearanceTests`'
    // `testTheSlotBudgetStaysInsideTheIconsWidth` is the reader.
    static let radius: CGFloat = 5
    static let overlap: CGFloat = 4
    /// Gap between adjacent project dot-groups. Non-private so
    /// MenuBarImageBuilder can reuse the exact same value for the gap
    /// between the dots and the quota bars in Combined style — otherwise
    /// that seam would visually read as a bigger gap than the ones between
    /// dot-groups themselves.
    static let groupGap: CGFloat = 6
    private static let height: CGFloat = 18
    private static let fontSize: CGFloat = 10
    /// The slot budget the icon used before #1955 made it a setting, and still
    /// the default `MenuBarMaxProjects` hands out — so an install that never
    /// opens Settings renders byte-identically.
    static let defaultMaxVisibleGroups = MenuBarMaxProjects.defaultValue
    /// Advance width of one digit of the session count in the aggregate dot's
    /// Menlo label. Measured against the rendered glyph, not derived.
    static let countDigitWidth: CGFloat = 6.5
    /// Width of the single `…` that stands in for every bucket past the
    /// budget.
    static let overflowWidth: CGFloat = 10
    private static let overflowFillHex = IrrSVG.cancelled
    /// Slice order for the per-group pie dot — DERIVED from `allCases`, never
    /// hand-listed (#1797).
    ///
    /// The fractions are `count / sessions.count`, so a state missing from this
    /// list is a session counted in the denominator with no wedge of its own
    /// (the dot renders with a hole), and a group of nothing but the missing
    /// state produced NO segments at all and fell through
    /// `aggregatedCircleElements`' single-segment path to a green dot. That is
    /// how `.unknown` broke it, and a literal array would let a 5th state break
    /// it again in exactly the same way — an array is the one state reader the
    /// Swift compiler cannot force to be exhaustive. Sorting `allCases` by
    /// `menuBarRank`, which IS a compiler-forced switch, moves the requirement
    /// somewhere it cannot be forgotten.
    private static let segmentOrder: [SessionState.State] =
        SessionState.State.allCases.sorted { $0.menuBarRank < $1.menuBarRank }

    static func buildStatusImage(
        sessions: [SessionState],
        projectGroupOrder: [String],
        maxGroups: Int = defaultMaxVisibleGroups
    ) -> NSImage? {
        image(from: buildStatusSVG(
            sessions: sessions, projectGroupOrder: projectGroupOrder, maxGroups: maxGroups
        ))
    }

    /// `maxGroups` is the whole-icon slot budget (#1955) — the number that was
    /// hardcoded at 5 before it became a setting. Everything past it collapses
    /// into one `renderOverflow()`, whatever the budget is, which is the
    /// mechanism that was already here rather than a new one.
    ///
    /// Clamped through `MenuBarMaxProjects` rather than trusted, and the two
    /// failures that establishes were OBSERVED by removing the clamp and
    /// running `MenuBarStatusRendererTests`
    /// (`testAnOutOfRangeSlotBudgetIsClampedRatherThanHonoured`), not
    /// predicted: a budget of `0` renders every project away and leaves a lone
    /// `…` as the whole icon, and a NEGATIVE budget traps outright — `Fatal
    /// error: Can't take a prefix of negative length from a collection`, in
    /// `prefix(budget)` below. `MenuBarAppearance` clamps at construction too,
    /// so the app cannot reach either; this is the seam a direct caller and a
    /// hand-edited plist reach.
    static func buildStatusSVG(
        sessions: [SessionState],
        projectGroupOrder: [String],
        maxGroups: Int = defaultMaxVisibleGroups
    ) -> (svg: String, width: CGFloat)? {
        assemble(
            budgeted(
                orderedProjectGroups(from: sessions, projectGroupOrder: projectGroupOrder),
                maxGroups: maxGroups
            ).renders
        )
    }

    /// Spend the whole-icon slot budget on a BUILT bucket list, and say what it
    /// spent it on.
    ///
    /// **It takes the built groups and never a name array, and that is the
    /// property, not an implementation detail.** #1956 made
    /// `projectGroupOrder` a remembered SUPERSET that is never pruned, so its
    /// LENGTH says nothing about how many buckets the icon draws; a budget or
    /// an overflow test read off that array pads both with names nobody has a
    /// session for. Extracted here by #1955 phase 2 so the two bucketings
    /// cannot acquire two differently-wrong budget rules — `location` has no
    /// name array to be tempted by in the first place, since
    /// `buildLocationStatusSVG` never receives one.
    ///
    /// Re-run rather than asserted: `MenuBarStatusRendererTests
    /// .testTheSupersetOrderMutantsAreAllRejected` drives three committed
    /// mutants of exactly that mistake through the same contract the shipped
    /// renderer satisfies, and `...testTheLocationBudgetIsSpentOnRendered`
    /// `BucketsNotDaemonLabels` does the same for this path's daemon map.
    ///
    /// - Returns: the renders to lay out, the labels of the buckets actually
    ///   drawn (in order), and how many buckets the single `…` stands for.
    private static func budgeted(
        _ groups: [(String, [SessionState])],
        maxGroups: Int
    ) -> (renders: [GroupRender], drawn: [String], hidden: Int) {
        let budget = MenuBarMaxProjects.clamp(maxGroups)
        let shown = Array(groups.prefix(budget))
        let hidden = groups.count - shown.count
        var renders = shown.map { renderGroup($0.1) }
        if hidden > 0 {
            renders.append(renderOverflow())
        }
        return (renders, shown.map(\.0), hidden)
    }

    // MARK: - Location grouping (#1955 phase 2): one bucket per daemon

    /// What the bucket for sessions with no `daemonID` is called — the ones
    /// this Mac's own daemon reported, which `SessionState.daemonID`'s
    /// declaration (`SessionState.swift:914`, `nil` for local) is the only
    /// thing distinguishing them.
    ///
    /// `"Local"` rather than a hostname because that is the word the app
    /// already uses for the same daemon in `SessionManager.connectionTooltip`
    /// (`lines.append("Local — \(connectionState.shortLabel)…")`), and
    /// because nothing under `platforms/macos/Irrlicht` reads a host name today
    /// — checked with `git grep -n 'Host.current\|hostName\|ProcessInfo.*host'`,
    /// which returns nothing. Inventing one here would put a second name for
    /// this machine in front of the user.
    static let localBucketLabel = "Local"

    /// The whole icon for `MenuBarGrouping.location` — one bucket per daemon,
    /// labelled by daemon.
    ///
    /// **It takes no `projectGroupOrder`, and that absence is the design.**
    /// That array is project-keyed and popover-owned, and #1956 made it a
    /// remembered superset that is never pruned: a daemon label written into it
    /// would be a phantom name surviving forever, padding the `count - 1` bound
    /// `SessionManager.reorderMoves(atIndex:in:)` measures against and handing
    /// the last visible row a chevron aimed at nothing — #1948 in a new
    /// costume, which is the regression #1949 fixed. Not receiving the array is
    /// what makes "derives its order and writes nothing" a fact about the
    /// signature rather than a promise about the body.
    /// `MenuBarAppearanceTests.testLocationGroupingNeverTouchesTheRemembered`
    /// `ProjectOrder` drives a real relay ingest through `RelayFixtures.push`
    /// and pins the array byte-identical across the render.
    ///
    /// The bucket ORDER is derived on every render, never persisted: local
    /// first, then daemons by the label the user reads, ascending. Persisting
    /// it would churn on every relay reconnect — the failure class #1954 is
    /// about.
    ///
    /// The names reach the user only while the DOTS are drawn. On the `.usage`
    /// style with a renderable quota the dot half is hidden by design
    /// (`MenuBarAppearance.hidesDotsWhenQuotaIsRenderable`), so there are no
    /// buckets on screen and nothing to name — `MenuBarImageBuilderTests`'
    /// `(.usage, .location, full)` row pins that composition. Noted because
    /// "the daemon names vanished on Usage" reads like a bug and is the
    /// grouping doing exactly what the style asks.
    static func buildLocationStatusImage(
        sessions: [SessionState],
        daemonLabels: [String: String],
        maxGroups: Int = defaultMaxVisibleGroups
    ) -> NSImage? {
        guard let render = buildLocationStatusSVG(
            sessions: sessions, daemonLabels: daemonLabels, maxGroups: maxGroups
        ) else { return nil }
        let image = image(from: (render.svg, render.width))
        // The daemon names have nowhere to go in the icon itself — dot-groups
        // carry no text — so they surface here, the seam `OffFlameImage`
        // already uses for the flame states (`OffFlameImage.swift:71`).
        image?.accessibilityDescription = render.accessibilityDescription
        return image
    }

    /// What `buildLocationStatusImage` rasterises, plus the name the buckets
    /// have no room to carry visually.
    ///
    /// One value rather than two entry points because the description must name
    /// the buckets the icon ACTUALLY DREW: computing "what was drawn" a second
    /// time is the divergence `assemble`'s doc comment records from #1849's
    /// review, where two loops over the same layout disagreed by 6pt with the
    /// whole suite green.
    struct LocationRender: Equatable {
        let svg: String
        let width: CGFloat
        let accessibilityDescription: String
    }

    static func buildLocationStatusSVG(
        sessions: [SessionState],
        daemonLabels: [String: String],
        maxGroups: Int = defaultMaxVisibleGroups
    ) -> LocationRender? {
        let spend = budgeted(
            orderedLocationGroups(from: sessions, daemonLabels: daemonLabels),
            maxGroups: maxGroups
        )
        guard let (svg, width) = assemble(spend.renders) else { return nil }
        return LocationRender(
            svg: svg,
            width: width,
            accessibilityDescription: locationAccessibilityDescription(
                drawn: spend.drawn, hidden: spend.hidden
            )
        )
    }

    /// Names the buckets the icon drew, in the order it drew them, and folds
    /// the ones it could not fit into the same single `…` the dots use.
    ///
    /// Non-private so a test can state the wording once; the property that
    /// matters — that it tracks what was DRAWN rather than what exists — is
    /// asserted against a real render, since only `budgeted` knows the
    /// difference.
    ///
    /// Two limits, stated rather than left to be rediscovered. **Two daemons
    /// reporting the same label read identically here** (`… on Local, laptop,
    /// laptop`); the bucket ORDER is still deterministic — `orderedLocationGroups`
    /// breaks the tie on the daemon id — but the spoken names do not
    /// disambiguate, and the relay only defaults a label to the id when one is
    /// absent, so nothing stops two hosts choosing the same one. **A relay
    /// daemon labelled `Local` collides with `localBucketLabel`** in the same
    /// way. Both are cosmetic and neither can mis-draw the icon.
    static func locationAccessibilityDescription(drawn: [String], hidden: Int) -> String {
        var parts = drawn
        if hidden > 0 {
            parts.append(hidden == 1 ? "1 more" : "\(hidden) more")
        }
        return "Irrlicht — sessions on \(parts.joined(separator: ", "))"
    }

    /// One bucket per daemon: local first, then relay daemons by label.
    ///
    /// Labels come in as one map, `SessionManager.daemonLabels`, which spans
    /// the connected and the faded daemons alike — so a disconnected daemon
    /// whose rows are still on screen (fade, don't delete — #540) keeps in its
    /// bucket the name those rows show, instead of reverting to a uuid. That
    /// accessor replaced the `relayDaemons[id] ?? offlineDaemons[id] ?? id`
    /// idiom `SessionRowView`'s cloud-glyph tooltip spelled inline; both read
    /// it now, which is what keeps the two from disagreeing.
    ///
    /// Mutation-proved (source), and written from the mutation as RUN: keying
    /// this on `session.projectName ?? session.cwd` instead — the per-project
    /// rule, which is the mistake that makes `location` a second name for
    /// `project` — takes **17 assertions across 10 test cases** red in three
    /// suites. Counted, not estimated, by applying the edit and running
    /// `swift test --skip LauncherTestHarness --skip LauncherHarnessTests`,
    /// then `grep -cE '^/.*error: -\['` over its output for the assertions and
    /// `grep -E '^Test Case .* failed' | sort -u | wc -l` for the cases. (An
    /// earlier draft of this comment said twelve, which was a stale count from
    /// before `testDotsImageRoutesEachGroupingToItsOwnRenderer` gained its
    /// vacuity guard — review caught it.) The sharpest two are the
    /// relay-ingest one:
    /// `("4") is not equal to ("3") - one bucket per daemon plus one local, not
    /// one per project`, and `("Irrlicht — sessions on alpha-one, alpha-two,
    /// beta-one, local-one") is not equal to ("Irrlicht — sessions on Local,
    /// alpha-box, beta-box")`.
    ///
    /// Sorted by the label the user reads, with the daemon id as a tie-break:
    /// two daemons may report the same label (the relay defaults it to the id
    /// only when absent, so nothing stops two hosts both calling themselves
    /// `laptop`), and without the tie-break their order would come out of
    /// `Dictionary`'s unspecified iteration order and could differ between two
    /// renders of the same state.
    private static func orderedLocationGroups(
        from sessions: [SessionState],
        daemonLabels: [String: String]
    ) -> [(String, [SessionState])] {
        var byDaemon: [String: [SessionState]] = [:]
        var local: [SessionState] = []
        for session in topLevelSessions(sessions) {
            if let id = session.daemonID {
                byDaemon[id, default: []].append(session)
            } else {
                local.append(session)
            }
        }

        var groups: [(String, [SessionState])] = []
        if !local.isEmpty {
            groups.append((localBucketLabel, local))
        }
        let relay = byDaemon.map {
            (label: daemonLabels[$0.key] ?? $0.key, id: $0.key, sessions: $0.value)
        }
        for daemon in relay.sorted(by: { ($0.label, $0.id) < ($1.label, $1.id) }) {
            groups.append((daemon.label, daemon.sessions))
        }
        return groups
    }

    // MARK: - Combined grouping (issue #1845's compact style, #1955's one bucket)

    /// Render EVERY top-level session as a single aggregate dot, ignoring
    /// project boundaries entirely — `MenuBarGrouping.combined`.
    ///
    /// This is the width fix. `buildStatusSVG` costs one render per bucket
    /// plus a `groupGap` between each, so its width grows with the number of
    /// projects until the slot budget caps it — by which point the icon
    /// can already be wide enough to sit behind the notch on a 13"/14" screen
    /// with a crowded menu bar. This function's width depends only on the
    /// number of DIGITS in the session count, so it is constant at 18.5pt for
    /// 1-9 sessions and 25pt for 10-99, no matter how many projects are open.
    ///
    /// **It stays its own entry point rather than becoming "one bucket through
    /// `buildStatusSVG`", and that is load-bearing.** `renderGroup` sends a
    /// bucket of 3 or fewer sessions to `renderCompactGroup` — overlapping
    /// plain dots — while this routes every session through `aggregateRender`,
    /// the pie plus count. Routing the combined bucket through the normal
    /// group renderer would therefore change what a migrated compact user sees
    /// at 3 sessions or fewer. Verified by reading both functions at
    /// `a680d5ea`; `MenuBarAppearanceTests.testMigratedCompactUsersKeepTheir`
    /// `Rendering` re-runs it at 2 sessions, where the two paths differ.
    static func buildAggregateStatusImage(sessions: [SessionState]) -> NSImage? {
        image(from: buildAggregateStatusSVG(sessions: sessions))
    }

    static func buildAggregateStatusSVG(sessions: [SessionState]) -> (svg: String, width: CGFloat)? {
        let topLevel = topLevelSessions(sessions)
        guard !topLevel.isEmpty else { return nil }
        return assemble([aggregateRender(topLevel)])
    }

    // MARK: - Shared assembly

    /// Lay renders out left to right with `groupGap` between them and wrap
    /// the result in one `<svg>`. Shared by the per-project and aggregate
    /// paths so the two cannot drift in how they space or size themselves.
    /// One loop, deliberately. The layout offsets and the declared total
    /// width are the same arithmetic, and they used to be computed by two
    /// separate loops each carrying its own `if index > 0` gap rule. Review
    /// of #1849 showed that dropping the gap from one of them left the
    /// declared width at 32 while the content laid out to 38 — clipping the
    /// last dot off the icon — with the whole suite green. Deriving the
    /// width from the same walk that places the groups makes that
    /// divergence unrepresentable.
    private static func assemble(_ renders: [GroupRender]) -> (svg: String, width: CGFloat)? {
        var body = ""
        var offsetX: CGFloat = 0
        for (index, render) in renders.enumerated() {
            if index > 0 {
                offsetX += groupGap
            }
            body += "<g transform=\"translate(\(svgNumber(offsetX)),0)\">\(render.elements)</g>"
            offsetX += render.width
        }

        let totalWidth = offsetX
        guard totalWidth > 0 else { return nil }

        let svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(totalWidth))" height="\(Int(height))">
        """ + body + "</svg>"

        return (svg, totalWidth)
    }

    private static func image(from built: (svg: String, width: CGFloat)?) -> NSImage? {
        guard let (svg, totalWidth) = built,
              let data = svg.data(using: .utf8),
              let image = NSImage(data: data) else {
            return nil
        }

        image.isTemplate = false
        image.size = NSSize(width: totalWidth, height: height)
        return image
    }

    static func stateSegments(for sessions: [SessionState]) -> [StateSegment] {
        let total = sessions.count
        guard total > 0 else { return [] }

        return segmentOrder.compactMap { state in
            let count = sessions.lazy.filter { $0.state == state }.count
            guard count > 0 else { return nil }
            return StateSegment(
                state: state,
                count: count,
                fraction: Double(count) / Double(total)
            )
        }
    }

    static func aggregatedGroupSVG(for sessions: [SessionState]) -> String {
        let circleElements = aggregatedCircleElements(for: sessions)
        let count = sessions.count
        let countStr = "\(count)"
        let textX = radius * 2 + 2
        let textY = (height / 2) + fontSize * 0.35
        let dominantHex = SessionState.State.dominant(in: sessions.map(\.state)).hexColor

        return """
        \(circleElements)
        <text x="\(svgNumber(textX))" y="\(svgNumber(textY))" font-family="Menlo,monospace" font-size="\(Int(fontSize))" font-weight="bold" fill="#\(dominantHex)">\(countStr)</text>
        """
    }

    /// The sessions the icon counts: top-level only, never a subagent or a
    /// background agent (those are linked to a parent via `parentSessionId`
    /// and are already represented by it).
    private static func topLevelSessions(_ sessions: [SessionState]) -> [SessionState] {
        sessions.filter { $0.parentSessionId == nil }
    }

    private static func orderedProjectGroups(
        from sessions: [SessionState],
        projectGroupOrder: [String]
    ) -> [(String, [SessionState])] {
        var groupMap: [String: [SessionState]] = [:]
        for session in topLevelSessions(sessions) {
            let key = session.projectName ?? session.cwd
            groupMap[key, default: []].append(session)
        }

        var groups: [(String, [SessionState])] = []
        var remaining = groupMap

        for key in projectGroupOrder {
            if let sessions = remaining.removeValue(forKey: key) {
                groups.append((key, sessions))
            }
        }

        for (key, sessions) in remaining.sorted(by: { $0.key < $1.key }) {
            groups.append((key, sessions))
        }

        return groups
    }

    private static func renderGroup(_ sessions: [SessionState]) -> GroupRender {
        if sessions.count <= 3 {
            return renderCompactGroup(sessions)
        }
        return aggregateRender(sessions)
    }

    /// One pie dot plus the session count. Used both for a single crowded
    /// project (>3 sessions) and, by `combined` grouping, for every project at
    /// once — so the two share this width arithmetic rather than repeating
    /// the digit-width constant.
    ///
    /// This is also the WIDEST a single slot can be, which is what makes the
    /// slot budget's worst case computable: `radius * 2 + 2 + digits *
    /// countDigitWidth`. `renderCompactGroup`, the other arm, is capped at 3
    /// sessions and therefore at `3 * (radius * 2 - overlap) + overlap` = 22pt,
    /// under this arm's 25pt for a two-digit count. Stays private — the width
    /// test states that arithmetic from the constants above and then proves it
    /// against a real render, rather than reaching in here for a private type.
    private static func aggregateRender(_ sessions: [SessionState]) -> GroupRender {
        let countWidth = CGFloat(String(sessions.count).count) * countDigitWidth
        let elements = aggregatedGroupSVG(for: sessions)
        let width = radius * 2 + 2 + countWidth
        return GroupRender(elements: elements, width: width)
    }

    private static func renderOverflow() -> GroupRender {
        let textY = (height / 2) + fontSize * 0.35
        let elements = """
        <text x="0" y="\(svgNumber(textY))" font-family="Menlo,monospace" font-size="\(Int(fontSize))" font-weight="bold" fill="#\(overflowFillHex)">…</text>
        """
        return GroupRender(elements: elements, width: overflowWidth)
    }

    private static func renderCompactGroup(_ sessions: [SessionState]) -> GroupRender {
        let cy = height / 2
        var elements = ""
        var x = radius

        for session in sessions {
            elements += """
            <circle cx="\(svgNumber(x))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="#\(session.state.hexColor)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
            """
            x += radius * 2 - overlap
        }

        let width = CGFloat(sessions.count) * (radius * 2 - overlap) + overlap
        return GroupRender(elements: elements, width: width)
    }

    private static func aggregatedCircleElements(for sessions: [SessionState]) -> String {
        let segments = stateSegments(for: sessions)
        let cx = radius
        let cy = height / 2

        guard segments.count > 1 else {
            // One state covers the whole group: a solid dot in its own hue,
            // no pie. `segments` cannot be empty here — segmentOrder spans
            // every case, so it is empty only for an empty session list, and
            // neither caller can pass one: renderGroup sends anything with
            // <= 3 sessions to renderCompactGroup, and buildAggregateStatusSVG
            // guards on `!topLevel.isEmpty` before it ever gets here (#1845 —
            // the Compact style is the second caller, and it deliberately
            // DOES route 1-3 sessions through the aggregate path, so the
            // first clause alone no longer covers every route in).
            // Returning "" rather than inventing a color
            // for the impossible case: a `?? .ready` here was one of the ways
            // an unreadable group used to paint green (#1797), and no fallback
            // hue is better than a wrong one.
            guard let only = segments.first else { return "" }
            return """
            <circle cx="\(svgNumber(cx))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="#\(only.state.hexColor)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
            """
        }

        var angle = -90.0
        var elements = ""
        for segment in segments {
            let sweep = 360.0 * segment.fraction
            let endAngle = angle + sweep
            elements += pieSliceSVG(
                centerX: cx,
                centerY: cy,
                radius: radius,
                startAngle: angle,
                endAngle: endAngle,
                fillHex: segment.state.hexColor
            )
            angle = endAngle
        }

        elements += """
        <circle cx="\(svgNumber(cx))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="none" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
        """
        return elements
    }

    private static func pieSliceSVG(
        centerX: CGFloat,
        centerY: CGFloat,
        radius: CGFloat,
        startAngle: Double,
        endAngle: Double,
        fillHex: String
    ) -> String {
        let start = point(onCircleWithCenterX: centerX, centerY: centerY, radius: radius, angle: startAngle)
        let end = point(onCircleWithCenterX: centerX, centerY: centerY, radius: radius, angle: endAngle)
        let sweep = endAngle - startAngle
        let largeArcFlag = sweep > 180.0 ? 1 : 0

        return """
        <path d="M \(svgNumber(centerX)) \(svgNumber(centerY)) L \(svgNumber(start.x)) \(svgNumber(start.y)) A \(svgNumber(radius)) \(svgNumber(radius)) 0 \(largeArcFlag) 1 \(svgNumber(end.x)) \(svgNumber(end.y)) Z" fill="#\(fillHex)" stroke="rgba(0,0,0,0.15)" stroke-width="0.35"/>
        """
    }

    private static func point(
        onCircleWithCenterX centerX: CGFloat,
        centerY: CGFloat,
        radius: CGFloat,
        angle: Double
    ) -> CGPoint {
        let radians = angle * .pi / 180
        return CGPoint(
            x: centerX + radius * CGFloat(cos(radians)),
            y: centerY + radius * CGFloat(sin(radians))
        )
    }

    private static func svgNumber(_ value: CGFloat) -> String {
        String(format: "%.2f", value)
    }
}
