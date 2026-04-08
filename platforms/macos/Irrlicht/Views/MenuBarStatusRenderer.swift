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
    private static let groupGap: CGFloat = 6
    private static let height: CGFloat = 18
    private static let fontSize: CGFloat = 10
    private static let segmentOrder: [SessionState.State] = [.waiting, .working, .ready]

    static func buildStatusImage(
        sessions: [SessionState],
        projectGroupOrder: [String]
    ) -> NSImage? {
        let groups = orderedProjectGroups(from: sessions, projectGroupOrder: projectGroupOrder)
        let renders = groups.prefix(8).map { renderGroup($0.1) }
        let totalWidth = totalRenderWidth(renders)

        guard totalWidth > 0 else { return nil }

        var svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(totalWidth))" height="\(Int(height))">
        """

        var offsetX: CGFloat = 0
        for (index, render) in renders.enumerated() {
            if index > 0 {
                offsetX += groupGap
            }
            svg += "<g transform=\"translate(\(svgNumber(offsetX)),0)\">\(render.elements)</g>"
            offsetX += render.width
        }
        svg += "</svg>"

        guard let data = svg.data(using: .utf8),
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

    private static func orderedProjectGroups(
        from sessions: [SessionState],
        projectGroupOrder: [String]
    ) -> [(String, [SessionState])] {
        var groupMap: [String: [SessionState]] = [:]
        for session in sessions where session.parentSessionId == nil {
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

        let countWidth = CGFloat(String(sessions.count).count) * 6.5
        let elements = aggregatedGroupSVG(for: sessions)
        let width = radius * 2 + 2 + countWidth
        return GroupRender(elements: elements, width: width)
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
            let hex = segments.first?.state.hexColor ?? SessionState.State.ready.hexColor
            return """
            <circle cx="\(svgNumber(cx))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="#\(hex)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
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

    private static func totalRenderWidth(_ renders: [GroupRender]) -> CGFloat {
        var totalWidth: CGFloat = 0
        for (index, render) in renders.enumerated() {
            if index > 0 {
                totalWidth += groupGap
            }
            totalWidth += render.width
        }
        return totalWidth
    }

    private static func svgNumber(_ value: CGFloat) -> String {
        String(format: "%.2f", value)
    }
}
