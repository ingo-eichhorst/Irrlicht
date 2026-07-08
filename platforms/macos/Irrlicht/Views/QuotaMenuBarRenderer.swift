import AppKit
import Foundation

/// Renders the subscription-quota mini-bars ("5h" / "7d") for the menu bar
/// status item — the Usage/Combined styles from issue #909. Shares
/// MenuBarStatusRenderer's 18pt-tall coordinate system so the two pieces
/// can sit side by side in MenuBarImageBuilder's composition.
@MainActor
enum QuotaMenuBarRenderer {
    private static let height: CGFloat = 18
    private static let rowHeight: CGFloat = height / 2
    private static let labelWidth: CGFloat = 15
    private static let barWidth: CGFloat = 32
    private static let barHeight: CGFloat = 5
    private static let fontSize: CGFloat = 8
    private static let gap: CGFloat = 3
    /// Bars style is narrowed by this factor (35% narrower) in Combined
    /// style specifically, where the icon's width budget is shared with the
    /// dots — see `rowSVG`'s `compact` handling.
    private static let compactBarWidthFactor: CGFloat = 0.65

    /// Picks the freshest renderable rate-limit snapshot for `providerKey`
    /// across `sessions` and renders it. `providerKey` nil means "whatever
    /// provider is freshest" — used until the user picks one in Settings.
    /// `compact` requests the narrower, label-less bars layout used in
    /// Combined style (ignored by the Circle visual style, which has no
    /// label to drop and is already compact).
    static func imageForSelectedProvider(
        sessions: [SessionState],
        providerKey: String?,
        compact: Bool = false
    ) -> NSImage? {
        guard let info = selectedSnapshot(sessions: sessions, providerKey: providerKey) else {
            return nil
        }
        return buildImage(for: info, compact: compact)
    }

    /// Freshest-`sampledAt`-wins across `sessions`, matching
    /// SessionListView.mergeIntoBuckets' choice of representative snapshot
    /// per provider. Unlike that bucketing, this does **not** drop stale
    /// snapshots (any window past `resetsAt`): the popover keeps a stale
    /// snapshot and dims the chip rather than blanking it, and the compact
    /// icon has no room to dim — so it keeps showing the last-known reading
    /// until the next statusline tick refreshes it, instead of disappearing
    /// (which would otherwise make an active `.usage`-style icon look idle;
    /// see MenuBarImageBuilder's fallback for the session-count side of that
    /// same problem). What *is* filtered out is a snapshot with no windows
    /// at all (the credits/usage-only path) — that can never render, so it
    /// must not win over an older snapshot that actually has data.
    static func selectedSnapshot(
        sessions: [SessionState],
        providerKey: String?
    ) -> RateLimitInfo? {
        let candidates: [(key: String, info: RateLimitInfo)] = sessions.compactMap { session in
            guard let snap = session.metrics?.rateLimit, !snap.windows.isEmpty else { return nil }
            let key = snap.providerKey(adapter: session.adapter) ?? "unknown:\(session.adapter ?? "")"
            return (key, snap)
        }
        let filtered = providerKey.map { key in candidates.filter { $0.key == key } } ?? candidates
        return filtered.max { $0.info.sampledAt < $1.info.sampledAt }?.info
    }

    static func buildImage(for info: RateLimitInfo, compact: Bool = false) -> NSImage? {
        let built: (svg: String, width: CGFloat)?
        switch QuotaVisualStyle.current {
        case .bars: built = buildSVG(for: info, compact: compact)
        case .circle: built = buildCircleSVG(for: info)
        }
        guard let (svg, width) = built else { return nil }
        guard let data = svg.data(using: .utf8), let image = NSImage(data: data) else { return nil }
        image.isTemplate = false
        image.size = NSSize(width: width, height: height)
        return image
    }

    /// `compact` drops the "5h"/"7d" text label and narrows the bars —
    /// used in Combined style, where the icon's width is shared with the
    /// session-state dots. Defaults to `false` (today's Usage-style layout).
    static func buildSVG(for info: RateLimitInfo, compact: Bool = false) -> (svg: String, width: CGFloat)? {
        let fiveHour = info.windows.first { $0.canonicalWindowMinutes == 300 }
        let sevenDay = info.windows.first { $0.canonicalWindowMinutes == 10080 }
        guard fiveHour != nil || sevenDay != nil else { return nil }

        let effectiveBarWidth = compact ? barWidth * compactBarWidthFactor : barWidth
        let width = compact ? effectiveBarWidth : labelWidth + gap + barWidth
        var svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(width))" height="\(Int(height))">
        """
        if let fiveHour {
            svg += rowSVG(label: "5h", window: fiveHour, rowY: 0, compact: compact)
        }
        if let sevenDay {
            svg += rowSVG(label: "7d", window: sevenDay, rowY: rowHeight, compact: compact)
        }
        svg += "</svg>"
        return (svg, width)
    }

    /// Single compact ring for the 5h window specifically — deliberately
    /// *not* RateLimitInfo.imminentWindow (which can jump to the 7d window
    /// once that's more depleted than the 5h one): a glance-value should
    /// stay pinned to one fixed window rather than silently swap which
    /// number it's showing. Falls back to 7d only when 5h is absent (e.g.
    /// a fresh snapshot that hasn't carried both windows yet).
    static func buildCircleSVG(for info: RateLimitInfo) -> (svg: String, width: CGFloat)? {
        let fiveHour = info.windows.first { $0.canonicalWindowMinutes == 300 }
        let sevenDay = info.windows.first { $0.canonicalWindowMinutes == 10080 }
        guard let window = fiveHour ?? sevenDay else { return nil }
        let pct = min(max(window.usedPercent, 0), 100) / 100
        let size = height // square, same 18pt row height as the bars/dots
        let cx = size / 2
        let cy = size / 2
        let radius = size / 2 - 2.25
        let strokeWidth: CGFloat = 2.5
        let pace = pacePercent(for: window)
        let hex = colorHex(usedPercent: window.usedPercent, pacePercent: pace)

        let circumference = 2 * Double.pi * Double(radius)
        let dashOffset = circumference * (1 - pct)

        var svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(size))" height="\(Int(size))">
          <circle cx="\(svgNumber(cx))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="none" stroke="\(trackColor)" stroke-width="\(svgNumber(strokeWidth))"/>
          <circle cx="\(svgNumber(cx))" cy="\(svgNumber(cy))" r="\(svgNumber(radius))" fill="none" stroke="#\(hex)" stroke-width="\(svgNumber(strokeWidth))" stroke-linecap="round" stroke-dasharray="\(String(format: "%.2f", circumference))" stroke-dashoffset="\(String(format: "%.2f", dashOffset))" transform="rotate(-90 \(svgNumber(cx)) \(svgNumber(cy)))"/>
        """
        if let pace {
            // Same origin as the fill arc (rotate(-90) = 12 o'clock at
            // pace 0) so a full lap back to the top means the window's
            // wall-clock time is up, independent of how much quota is
            // actually used.
            let angle = (-90.0 + 360.0 * (pace / 100.0)) * .pi / 180
            let innerR = radius - strokeWidth / 2 - 0.75
            let outerR = radius + strokeWidth / 2 + 0.75
            let x1 = cx + innerR * CGFloat(cos(angle))
            let y1 = cy + innerR * CGFloat(sin(angle))
            let x2 = cx + outerR * CGFloat(cos(angle))
            let y2 = cy + outerR * CGFloat(sin(angle))
            svg += """
            <line x1="\(svgNumber(x1))" y1="\(svgNumber(y1))" x2="\(svgNumber(x2))" y2="\(svgNumber(y2))" stroke="red" stroke-width="1"/>
            """
        }
        svg += "</svg>"
        return (svg, size)
    }

    /// `compact` omits the leading "5h"/"7d" label and narrows the bar by
    /// `compactBarWidthFactor` — see `buildSVG`'s doc.
    private static func rowSVG(label: String, window: RateLimitWindowInfo, rowY: CGFloat, compact: Bool = false) -> String {
        let effectiveBarWidth = compact ? barWidth * compactBarWidthFactor : barWidth
        let pct = min(max(window.usedPercent, 0), 100) / 100
        let filledWidth = effectiveBarWidth * pct
        let barX: CGFloat = compact ? 0 : labelWidth + gap
        let barY = rowY + (rowHeight - barHeight) / 2
        let textY = rowY + rowHeight * 0.78
        let pace = pacePercent(for: window)
        let hex = colorHex(usedPercent: window.usedPercent, pacePercent: pace)

        var svg = ""
        if !compact {
            svg += """
            <text x="0" y="\(svgNumber(textY))" font-family="Menlo,monospace" font-size="\(Int(fontSize))" fill="\(labelColor)">\(label)</text>
            """
        }
        svg += """
        <rect x="\(svgNumber(barX))" y="\(svgNumber(barY))" width="\(svgNumber(effectiveBarWidth))" height="\(svgNumber(barHeight))" rx="1.5" fill="\(trackColor)"/>
        <rect x="\(svgNumber(barX))" y="\(svgNumber(barY))" width="\(svgNumber(filledWidth))" height="\(svgNumber(barHeight))" rx="1.5" fill="#\(hex)"/>
        """
        // Pace marker (mirrors SessionListView.quotaPacePercent): reaching
        // the bar's right edge means the window's wall-clock time is up,
        // independent of the fill's used% value.
        if let pace {
            let paceX = barX + effectiveBarWidth * pace / 100
            svg += """
            <rect x="\(svgNumber(paceX - 0.5))" y="\(svgNumber(barY - 0.75))" width="1" height="\(svgNumber(barHeight + 1.5))" fill="red"/>
            """
        }
        return svg
    }

    /// Delegates to SessionListView.quotaPacePercent — same implementation
    /// the popover chip uses, not a second copy — so "where you'd be if
    /// usage had grown linearly since the window opened" can't drift between
    /// the two. Reachable here because `selectedSnapshot` no longer drops
    /// stale snapshots.
    private static func pacePercent(for window: RateLimitWindowInfo) -> Double? {
        SessionListView.quotaPacePercent(window)
    }

    /// Delegates to SessionListView.quotaColorTier — the same pace-aware
    /// ramp decision the popover chip's `barColor` uses, not a second
    /// hand-synced copy — so the same window can't read green in the icon
    /// while the popover shows it orange. Returns a bare hex (no '#') since
    /// callers splice it into SVG fill attributes; SessionListView's
    /// `barColor` maps the same tier to a SwiftUI `Color` instead, since
    /// it's used in a View.
    private static func colorHex(usedPercent: Double, pacePercent: Double?) -> String {
        switch SessionListView.quotaColorTier(used: usedPercent, pace: pacePercent) {
        case .green: return IrrSVG.ready
        case .yellow: return systemYellowHex
        case .orange: return systemOrangeHex
        }
    }

    // Bare hex for the two ramp colors SessionListView expresses as SwiftUI
    // .orange / .yellow (system colors, not in IrrHex/IrrSVG). .orange
    // already matches IrrHex.pressureMedium's value; kept as an explicit
    // literal here since the naming ("pressureMedium") doesn't fit the
    // pace-ramp vocabulary.
    private static let systemOrangeHex = "FF9500"
    private static let systemYellowHex = "FFCC00"

    /// True when the app's effective appearance is dark — same signal
    /// SessionState.adapterIcon already uses to pick a light/dark SVG
    /// variant, kept consistent here rather than introducing a second way
    /// to ask the same question — including its nil-fallback: NSApp is nil
    /// in unit tests, and adapterIcon defaults to the light variant there
    /// (the more common ambient appearance), so this matches rather than
    /// disagreeing with it.
    private static var isDarkAppearance: Bool {
        guard let app = NSApp else { return false }
        return app.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
    }

    /// Track (unfilled bar / ring) and label colors, appearance-aware: the
    /// original translucent-white track and light-gray label were invisible
    /// against a light menu bar (issue found in review — the dots renderer
    /// avoids this by using only saturated fills, which this renderer can't
    /// since the track must read as "empty" against the fill color).
    private static var trackColor: String {
        isDarkAppearance ? "rgba(255,255,255,0.18)" : "rgba(0,0,0,0.14)"
    }
    private static var labelColor: String {
        isDarkAppearance ? "#9CA3AF" : "#6B7280"
    }

    private static func svgNumber(_ value: CGFloat) -> String {
        String(format: "%.2f", value)
    }
}
