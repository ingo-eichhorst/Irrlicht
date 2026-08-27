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

    private static let radius: CGFloat = 5
    private static let overlap: CGFloat = 4
    /// Gap between adjacent project dot-groups. Non-private so
    /// MenuBarImageBuilder can reuse the exact same value for the gap
    /// between the dots and the quota bars in Combined style — otherwise
    /// that seam would visually read as a bigger gap than the ones between
    /// dot-groups themselves.
    static let groupGap: CGFloat = 6
    private static let height: CGFloat = 18
    private static let fontSize: CGFloat = 10
    private static let maxVisibleGroups = 5
    /// Advance width of one digit of the session count in the aggregate dot's
    /// Menlo label. Measured against the rendered glyph, not derived.
    private static let countDigitWidth: CGFloat = 6.5
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
        projectGroupOrder: [String]
    ) -> NSImage? {
        image(from: buildStatusSVG(sessions: sessions, projectGroupOrder: projectGroupOrder))
    }

    static func buildStatusSVG(
        sessions: [SessionState],
        projectGroupOrder: [String]
    ) -> (svg: String, width: CGFloat)? {
        let groups = orderedProjectGroups(from: sessions, projectGroupOrder: projectGroupOrder)
        var renders = groups.prefix(maxVisibleGroups).map { renderGroup($0.1) }
        if groups.count > maxVisibleGroups {
            renders.append(renderOverflow())
        }
        return assemble(renders)
    }

    // MARK: - Compact style (issue #1845)

    /// Render EVERY top-level session as a single aggregate dot, ignoring
    /// project boundaries entirely.
    ///
    /// This is the width fix. `buildStatusSVG` costs one render per project
    /// plus a `groupGap` between each, so its width grows with the number of
    /// projects until `maxVisibleGroups` caps it — by which point the icon
    /// can already be wide enough to sit behind the notch on a 13"/14" screen
    /// with a crowded menu bar. This function's width depends only on the
    /// number of DIGITS in the session count, so it is constant at 18.5pt for
    /// 1-9 sessions and 25pt for 10-99, no matter how many projects are open.
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
    /// project (>3 sessions) and, by the Compact style, for every project at
    /// once — so the two share this width arithmetic rather than repeating
    /// the digit-width constant.
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
        return GroupRender(elements: elements, width: 10)
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
