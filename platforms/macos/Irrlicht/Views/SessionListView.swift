import AppKit
import SwiftUI

// MARK: - Tooltip
//
// SwiftUI overlays are children of the host window's content layer, which the
// MenuBarController clips to a rounded rectangle (`masksToBounds = true`). Any
// SwiftUI-rendered tooltip that's wider than the hovered element gets cropped
// at the panel edge. NSView.toolTip likewise doesn't fire here because
// NSToolTipManager hit-tests the cursor's view, and the bridge's
// click-through hitTest=nil makes it invisible to that lookup.
//
// The fix is what AppKit itself does: render the tooltip in its own borderless
// nonactivating panel, positioned in screen coordinates above the cursor.

@MainActor
private final class TooltipWindowController {
    static let shared = TooltipWindowController()

    private let panel: NSPanel
    private let label: NSTextField

    private init() {
        panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 100, height: 30),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: true
        )
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        // Z-order vs the host panel is enforced via addChildWindow(_:ordered:.above)
        // in show(...) — that guarantees the tooltip renders above the host
        // regardless of level. Level only matters when there is no host.
        panel.level = NSWindow.Level(rawValue: 200)
        panel.ignoresMouseEvents = true
        panel.becomesKeyOnlyIfNeeded = true
        panel.hidesOnDeactivate = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient]
        panel.animationBehavior = .none

        label = NSTextField(labelWithString: "")
        label.font = .systemFont(ofSize: 11)
        label.textColor = .labelColor
        label.backgroundColor = .clear
        label.isBezeled = false
        label.isEditable = false
        label.isSelectable = false
        label.lineBreakMode = .byWordWrapping
        label.maximumNumberOfLines = 0
        label.translatesAutoresizingMaskIntoConstraints = false

        let bg = NSVisualEffectView()
        bg.material = .toolTip
        bg.blendingMode = .behindWindow
        bg.state = .active
        bg.wantsLayer = true
        bg.layer?.cornerRadius = 4
        bg.layer?.borderWidth = 0.5
        bg.layer?.borderColor = NSColor.separatorColor.cgColor
        bg.layer?.masksToBounds = true

        bg.addSubview(label)
        NSLayoutConstraint.activate([
            label.leadingAnchor.constraint(equalTo: bg.leadingAnchor, constant: 6),
            label.trailingAnchor.constraint(equalTo: bg.trailingAnchor, constant: -6),
            label.topAnchor.constraint(equalTo: bg.topAnchor, constant: 3),
            label.bottomAnchor.constraint(equalTo: bg.bottomAnchor, constant: -3),
        ])

        panel.contentView = bg
    }

    func show(text: String, near cursor: NSPoint) {
        label.stringValue = text
        let maxWidth: CGFloat = 280
        label.preferredMaxLayoutWidth = maxWidth
        let labelSize = label.sizeThatFits(NSSize(width: maxWidth, height: .greatestFiniteMagnitude))
        let size = NSSize(width: ceil(labelSize.width) + 12, height: ceil(labelSize.height) + 6)

        // Native macOS tooltips appear diagonally below-right of the cursor.
        // Screen Y grows upward, so "below cursor" = lower Y.
        var origin = NSPoint(x: cursor.x + 14, y: cursor.y - size.height - 18)
        // Clamp to the screen the cursor is actually on, not NSScreen.main —
        // otherwise the tooltip jumps to the focused display on multi-monitor.
        let cursorScreen = NSScreen.screens.first { $0.frame.contains(cursor) } ?? NSScreen.main
        if let visible = cursorScreen?.visibleFrame {
            origin.x = max(visible.minX + 4, min(origin.x, visible.maxX - size.width - 4))
            if origin.y < visible.minY + 4 {
                origin.y = cursor.y + 18  // flip above when no room below
            }
            origin.y = min(origin.y, visible.maxY - size.height - 4)
        }
        panel.setFrame(NSRect(origin: origin, size: size), display: true)
        // Parent the tooltip to whichever window is on top (main panel, or a
        // sheet presented on top of it). AppKit guarantees children render
        // above their parent in z-order — this is the only mechanism that
        // works reliably for two nonactivating panels in the same process.
        if let host = findHostWindow(), panel.parent !== host {
            panel.parent?.removeChildWindow(panel)
            host.addChildWindow(panel, ordered: .above)
        }
        panel.orderFrontRegardless()
    }

    func hide() {
        panel.orderOut(nil)
    }

    private func findHostWindow() -> NSWindow? {
        // orderedWindows is front-to-back; first non-tooltip visible window
        // is whichever the user is currently interacting with.
        NSApp.orderedWindows.first { $0 !== panel && $0.isVisible }
    }
}

private struct TooltipModifier: ViewModifier {
    let text: String
    @State private var hoverTask: Task<Void, Never>?

    func body(content: Content) -> some View {
        content.onHover { hovering in
            hoverTask?.cancel()
            if hovering, !text.isEmpty {
                hoverTask = Task { @MainActor in
                    try? await Task.sleep(nanoseconds: 700_000_000)
                    if !Task.isCancelled {
                        TooltipWindowController.shared.show(
                            text: text,
                            near: NSEvent.mouseLocation
                        )
                    }
                }
            } else {
                TooltipWindowController.shared.hide()
            }
        }
    }
}

extension View {
    func tooltip(_ text: String) -> some View {
        modifier(TooltipModifier(text: text))
    }
}

enum DisplayMode: String, CaseIterable {
    case context   = "Context"
    case history1s  = "1 Min"
    case history10s = "10 Min"
    case history60s = "60 Min"

    var isHistory: Bool { self != .context }

    var granularitySec: Int {
        switch self {
        case .context, .history1s:  return 1
        case .history10s:           return 10
        case .history60s:           return 60
        }
    }

    func next() -> DisplayMode {
        let all = Self.allCases
        return all[((all.firstIndex(of: self) ?? 0) + 1) % all.count]
    }

    var tooltip: String {
        switch self {
        case .context:    return "Context utilization (click to cycle to history view)"
        case .history1s:  return "Activity over the last 1 minute (60 buckets × 1s)"
        case .history10s: return "Activity over the last 10 minutes (60 buckets × 10s)"
        case .history60s: return "Activity over the last 60 minutes (60 buckets × 1min)"
        }
    }
}

struct SessionListView: View {
    /// Canonical panel width. Referenced by `MenuBarController` for the
    /// initial NSPanel contentRect so SwiftUI's `.frame(width:)` and the
    /// panel placeholder size can't drift apart.
    static let panelWidth: CGFloat = 380

    @EnvironmentObject var sessionManager: SessionManager
    @EnvironmentObject var gasTownProvider: GasTownProvider
    @State private var isQuitButtonHovered = false
    @State private var isSettingsButtonHovered = false
    @State private var showSettings = false
    @AppStorage("displayMode") private var displayModeRaw: String = DisplayMode.context.rawValue
    @AppStorage("showQuotaForecast") private var showQuotaForecast: Bool = true

    private var displayMode: DisplayMode { DisplayMode(rawValue: displayModeRaw) ?? .context }

    var body: some View {
        if showSettings {
            SettingsView(isPresented: $showSettings)
        } else {
            VStack(alignment: .leading, spacing: 0) {
                if sessionManager.apiGroups.isEmpty && sessionManager.sessions.isEmpty {
                    emptyStateView
                } else {
                    sessionHeaderView
                    Divider()
                    groupListContent
                }

                if let error = sessionManager.lastError {
                    Divider()
                    errorView(error)
                }

                Divider()
                HStack(spacing: 0) {
                    Button(action: { showSettings = true }) {
                        Text("Settings\u{2026}")
                            .foregroundColor(.secondary)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(isSettingsButtonHovered ? IrrColors.surfaceHover : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isSettingsButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)
                    .tooltip("Open settings panel")

                    Divider().frame(height: 20)

                    Button(action: { NSApplication.shared.terminate(nil) }) {
                        Text("Quit")
                            .foregroundColor(.secondary)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(isQuitButtonHovered ? IrrColors.surfaceHover : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isQuitButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)
                    .tooltip("Quit Irrlicht")
                }
            }
            .frame(width: Self.panelWidth)
            .background(Color(NSColor.windowBackgroundColor))
        }
    }

    // MARK: - Group List (renders apiGroups directly)

    // Cap for the session-list area. Below this, the panel grows with content.
    // Above this, the panel locks at 560pt and the list scrolls internally.
    private static let groupListMaxHeight: CGFloat = 560

    private var groupListContent: some View {
        // Single ScrollView with .fixedSize(vertical:) — reports its
        // content's ideal height up to the parent, so the MenuBarExtra
        // popover sizes itself to exactly that (capped at 560pt).
        // Collapsing a group shrinks the ideal height, which propagates
        // dynamically with no branch-switching (the source of flicker
        // the user saw).
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(sessionManager.apiGroups) { group in
                    GroupView(group: group)
                }
            }
        }
        .frame(maxHeight: Self.groupListMaxHeight)
        .fixedSize(horizontal: false, vertical: true)
        // Kill any inherited animation on this subtree — any fade/slide
        // transition on collapse races the popover's own resize and
        // produces a flash.
        .transaction { $0.disablesAnimations = true }
    }
    
    // MARK: - Empty State

    private var emptyStateView: some View {
        VStack(spacing: 8) {
            Image(nsImage: OffFlameImage.overlaySlashed)

            Text("No coding agent sessions detected")
                .font(.headline)
                .foregroundColor(.secondary)

            Text("Start a coding agent session to see it here.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(20)
    }

    // MARK: - Session Header

    private var sessionHeaderView: some View {
        HStack {
            HStack(spacing: 6) {
                headerTitleView

                // Em-dash + glyphs are visually redundant once any quota
                // chip is filling the slot — the chip already implies
                // session activity. Hide them while a chip is present.
                if quotaChipData.isEmpty || !showQuotaForecast {
                    Text("—")
                        .foregroundColor(.secondary)

                    sessionIconsView
                }
            }

            Spacer()

            Button {
                let next = displayMode.next()
                displayModeRaw = next.rawValue
                if next.isHistory {
                    sessionManager.setHistoryGranularity(next.granularitySec)
                }
            } label: {
                Text(displayMode.rawValue)
                    .font(.system(size: 10, design: .monospaced))
                    .frame(width: 44)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(displayMode.isHistory ? IrrColors.working.opacity(0.15) : Color.clear)
            .cornerRadius(IrrRadius.sm)
            .overlay(RoundedRectangle(cornerRadius: IrrRadius.sm).stroke(Color.secondary.opacity(0.4)))
            .contentShape(Rectangle())
            .tooltip(displayMode.tooltip)
            .id("mode-cycle-btn")

            statusIndicator
        }
        .padding(.horizontal, IrrSpacing.sp3)
        .padding(.vertical, IrrSpacing.sp2)
        .onAppear {
            if displayMode.isHistory {
                sessionManager.setHistoryGranularity(displayMode.granularitySec)
            }
        }
    }
    
    /// Header slot shown to the left of the session glyphs. Renders one
    /// chip per subscription provider whose sessions have surfaced quota
    /// data (Anthropic, OpenAI, …) — matching mockups 2 and 3 in
    /// issue #309. When more chips exist than the 380pt header can fit
    /// comfortably, the first `maxVisibleChips` render normally and the
    /// rest collapse into a single "+N more" chip whose tooltip lists
    /// what's hidden (mockup 3). Empty when no provider has a snapshot;
    /// the app version lives in Settings rather than competing for
    /// this slot.
    @ViewBuilder
    private var headerTitleView: some View {
        if showQuotaForecast {
            let chips = quotaChipData
            if !chips.isEmpty {
                let visible = Array(chips.prefix(Self.maxVisibleQuotaChips))
                let hidden = Array(chips.dropFirst(Self.maxVisibleQuotaChips))
                HStack(alignment: .top, spacing: 8) {
                    ForEach(visible) { chip in
                        quotaChipView(chip, compact: chips.count > 1)
                    }
                    if !hidden.isEmpty {
                        quotaOverflowChip(hidden: hidden)
                    }
                }
            } else {
                EmptyView()
            }
        } else {
            EmptyView()
        }
    }

    /// Cap on chips rendered inline in the header before overflow kicks
    /// in. Two compact chips at ~110pt each plus an 8pt gap is already
    /// most of the 380pt panel width once the mode button and status
    /// indicator are factored in; a third would overflow visibly.
    private static let maxVisibleQuotaChips = 2

    /// The "+N more" chip: a small grey pill showing the hidden chip
    /// count, whose tooltip lists each hidden provider with its
    /// headline metric. Matches mockup 3 in issue #309.
    @ViewBuilder
    private func quotaOverflowChip(hidden: [QuotaWidgetData]) -> some View {
        Text("+\(hidden.count) more")
            .font(.system(size: 10, weight: .medium, design: .monospaced))
            .foregroundColor(.secondary)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Color.secondary.opacity(0.15))
            .cornerRadius(IrrRadius.sm)
            .tooltip(hidden.map { quotaOverflowSummary($0) }.joined(separator: "\n"))
    }

    /// One line in the overflow tooltip: "<plan or provider>: <headline>".
    /// Subscription chips show the imminent window's percent; usage chips
    /// show the cumulative spend headline.
    private func quotaOverflowSummary(_ d: QuotaWidgetData) -> String {
        let label = d.snapshot.planTypeLabel ?? d.id.capitalized
        switch d.mode {
        case .subscription:
            if let imm = d.imminent {
                return "\(label): \(Int(imm.usedPercent.rounded()))%"
            }
            return label
        case .usage:
            let cost = d.totalCostUSD > 0 ? formatUsageCost(d.totalCostUSD) : "—"
            return "\(label): \(cost)"
        }
    }

    /// Render mode for a provider chip. Subscription chips show the
    /// 5h/7d bars; usage chips show cumulative spend across all
    /// sessions on that provider.
    private enum QuotaMode {
        case subscription
        case usage
    }

    /// One chip's worth of data: render mode, a representative snapshot
    /// for icon + tooltip lookup, the session that carries it, the
    /// most-imminent window (subscription only), and the summed spend
    /// (usage only). `id` is the providerKey so SwiftUI ForEach has a
    /// stable identity.
    private struct QuotaWidgetData: Identifiable {
        let id: String
        let mode: QuotaMode
        let snapshot: RateLimitInfo
        let session: SessionState
        let imminent: RateLimitWindowInfo?
        let totalCostUSD: Double
    }

    /// All chips to render, one per subscription/usage provider. See
    /// `bucketForChips` for the bucketing rules.
    private var quotaChipData: [QuotaWidgetData] {
        var byProvider: [String: ChipBucket] = [:]
        for session in sessionManager.sessions {
            mergeIntoBuckets(session: session, into: &byProvider)
        }
        return byProvider
            .map { id, b in b.toWidgetData(id: id) }
            .sorted { $0.id < $1.id }
    }

    /// Mutable accumulator for one provider bucket while folding
    /// sessions into chips.
    private struct ChipBucket {
        var snapshot: RateLimitInfo
        var session: SessionState
        var imminent: RateLimitWindowInfo?
        var totalCostUSD: Double
        var mode: QuotaMode

        func toWidgetData(id: String) -> QuotaWidgetData {
            QuotaWidgetData(
                id: id,
                mode: mode,
                snapshot: snapshot,
                session: session,
                imminent: imminent,
                totalCostUSD: totalCostUSD
            )
        }
    }

    /// Fold one session's snapshot into the provider buckets.
    ///
    /// Skips sessions without a snapshot and snapshots with any window
    /// past `resetsAt` — same rule as the daemon-side stale check, but
    /// applied here too because persisted ready sessions retain their
    /// last-known data on disk and bypass the daemon recompute.
    ///
    /// Within a bucket the representative snapshot is the **freshest**
    /// sample (largest `sampledAt`). An earlier rule — "highest
    /// `usedPercent`" — locked the chip onto stale ready sessions: a
    /// finished Anthropic session whose 5h window had rolled over at
    /// 16% would beat every fresh active session reading 2% post-
    /// rollover, leaving the chip stuck on the bad data forever. The
    /// bucket is account-scoped so all live sessions on a single
    /// account report identical numbers anyway; freshness only matters
    /// when one session has gone stale.
    ///
    /// For usage chips, `totalCostUSD` sums cumulative
    /// `estimatedCostUSD` across every matching session — close enough
    /// to "what the user has spent on this provider lately" for the
    /// typical session-lifetime. Proper daily rollup needs a
    /// daemon-side per-provider cost tracker (issue #385).
    private func mergeIntoBuckets(session: SessionState, into buckets: inout [String: ChipBucket]) {
        guard let snap = session.metrics?.rateLimit else { return }
        if snap.windows.contains(where: { $0.resetsAt <= Date() }) { return }
        let key = snap.providerKey(adapter: session.adapter)
            ?? "unknown:\(session.adapter ?? "")"
        let mode = resolveChipMode(snap: snap, providerKey: key)
        let imminent = snap.imminentWindow
        let sessionCost = session.metrics?.estimatedCostUSD ?? 0
        if var existing = buckets[key] {
            // Subscription wins over usage when both paths are seen
            // (rare — one OAuth account on both subscription and API
            // key): the bars are the richer signal.
            if existing.mode == .usage && mode == .subscription {
                existing.mode = .subscription
                existing.snapshot = snap
                existing.session = session
                existing.imminent = imminent
            } else if snap.sampledAt > existing.snapshot.sampledAt {
                existing.snapshot = snap
                existing.session = session
                existing.imminent = imminent
            }
            existing.totalCostUSD += sessionCost
            buckets[key] = existing
        } else {
            buckets[key] = ChipBucket(
                snapshot: snap,
                session: session,
                imminent: imminent,
                totalCostUSD: sessionCost,
                mode: mode
            )
        }
    }

    /// Resolve which chip variant to render for a snapshot, honouring
    /// the user's per-provider preference (Settings → Providers) and
    /// falling back to auto-detection from snapshot shape.
    private func resolveChipMode(snap: RateLimitInfo, providerKey: String) -> QuotaMode {
        let preference = ProviderModePreference.current(for: providerKey)
        switch preference {
        case .subscription: return .subscription
        case .usage: return .usage
        case .auto:
            return (snap.credits != nil && (snap.planType ?? "").isEmpty)
                ? .usage
                : .subscription
        }
    }

    /// The chip-style header widget. Dispatches on mode:
    ///   - subscription → provider icon + stacked 5h/7d bars (mockup 1/2)
    ///   - usage        → provider icon + cumulative spend (mockup 2)
    @ViewBuilder
    private func quotaChipView(_ d: QuotaWidgetData, compact: Bool) -> some View {
        HStack(spacing: 6) {
            // Provider icon (Anthropic / OpenAI) when we can infer one;
            // otherwise fall back to the adapter icon so the chip never
            // appears iconless. The quota bucket is provider-scoped, so
            // the provider mark is the more meaningful brand.
            //
            // `.resizable().frame(...)` is load-bearing: `NSImage(data:)`
            // on an SVG decodes inconsistently depending on the path's
            // complexity — the Anthropic single-path mark lands at the
            // SVG's declared 14×14 size, but the OpenAI multi-path knot
            // decoded at viewBox-native 24×24, dominating the chip and
            // pushing the body out of view. Forcing the SwiftUI frame
            // normalises both regardless of underlying decode quirks.
            if let icon = ProviderIconRegistry.image(forKey: d.snapshot.providerKey(adapter: d.session.adapter))
                ?? d.session.adapterIcon {
                Image(nsImage: icon)
                    .resizable()
                    .renderingMode(.template)
                    .frame(width: 14, height: 14)
                    .foregroundColor(.primary)
            }
            switch d.mode {
            case .subscription:
                VStack(alignment: .leading, spacing: 1) {
                    ForEach(d.snapshot.windows, id: \.windowMinutes) { window in
                        quotaWindowRow(window, compact: compact)
                    }
                }
            case .usage:
                quotaUsageBody(d, compact: compact)
            }
        }
        .tooltip(quotaTooltip(d))
    }

    /// Usage-mode chip body — cumulative spend across all sessions on
    /// this provider, with a "spend" subtitle. Always 2 lines so the
    /// chip has the same height as the subscription variant (5h + 7d
    /// rows) and the two render cleanly side-by-side in the header.
    ///
    /// Zero-state: when no spend has accumulated, render an em-dash and
    /// "no spend yet" rather than "$0.00 / spend". The data path is
    /// wired up, just nothing to report — important when a user has
    /// forced-usage mode on a subscription-only session, where
    /// `totalCostUSD` legitimately stays at zero.
    ///
    /// A `minWidth: 88` matches the subscription chip's row width
    /// (label + 40pt bar + percent) so a usage chip with a short
    /// headline doesn't collapse to an icon-only sliver next to a
    /// bars chip.
    @ViewBuilder
    private func quotaUsageBody(_ d: QuotaWidgetData, compact: Bool) -> some View {
        let hasSpend = d.totalCostUSD > 0
        VStack(alignment: .leading, spacing: 1) {
            Text(hasSpend ? formatUsageCost(d.totalCostUSD) : "—")
                .font(.system(size: 10, weight: .semibold, design: .monospaced))
                .foregroundColor(.primary)
            Text(hasSpend ? "spend" : "no spend yet")
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(.secondary)
        }
        .frame(minWidth: 88, alignment: .leading)
    }

    /// Format the cost headline: tiny costs render `<$0.01`, normal
    /// costs render with two decimals, ≥ $100 drops to integer dollars
    /// to keep the chip from growing.
    private func formatUsageCost(_ cost: Double) -> String {
        if cost <= 0 { return "$0.00" }
        if cost < 0.01 { return "<$0.01" }
        if cost >= 100 { return String(format: "$%.0f", cost) }
        return String(format: "$%.2f", cost)
    }

    /// One row inside a chip. In compact mode (multiple chips visible)
    /// the inline reset time is dropped — it lives in the tooltip — and
    /// the bar shrinks so two or three chips fit in the 380pt header.
    @ViewBuilder
    private func quotaWindowRow(_ w: RateLimitWindowInfo, compact: Bool) -> some View {
        HStack(spacing: 6) {
            Text(quotaWindowLabel(w.windowMinutes))
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.secondary)
                .frame(width: 14, alignment: .leading)

            quotaBar(percent: w.usedPercent, color: barColor(w.usedPercent))
                .frame(width: compact ? 40 : 70, height: 5)

            Text("\(Int(w.usedPercent.rounded()))%")
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.primary)
                .frame(width: 28, alignment: .trailing)

            if !compact {
                Text("resets \(formatResetTime(w.resetsAt))")
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
        }
    }

    /// A simple rounded-rect progress bar. ZStack so the fill and the
    /// track render with the same corner radius without clipping
    /// artifacts at small sizes.
    @ViewBuilder
    private func quotaBar(percent: Double, color: Color) -> some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(Color.secondary.opacity(0.2))
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(color)
                    .frame(width: geo.size.width * min(1.0, max(0.0, percent / 100.0)))
            }
        }
    }

    private func quotaTooltip(_ d: QuotaWidgetData) -> String {
        var lines: [String] = []
        if let plan = d.snapshot.planTypeLabel {
            lines.append(plan)
        }
        switch d.mode {
        case .subscription:
            for w in d.snapshot.windows {
                // Provider data is integer-precision (any decimals are
                // floating-point noise from a JSON marshal/unmarshal
                // round-trip on the daemon side, e.g. 7.000000000000001
                // for a value the provider reported as 7). Render as
                // whole percent so the tooltip matches the chip body.
                let pct = "\(Int(w.usedPercent.rounded()))%"
                let label = quotaWindowLabel(w.windowMinutes)
                let resets = formatTimeUntil(w.resetsAt)
                lines.append("\(label): \(pct) · resets in \(resets)")
            }
            if let eta = d.session.metrics?.rateLimitForecastEta {
                lines.append("Projected cap: \(formatClockTime(eta))")
            } else if d.snapshot.windows.contains(where: { $0.usedPercent > 0 }) {
                lines.append("Forecast: won't hit cap this window")
            }
        case .usage:
            lines.append("\(formatUsageCost(d.totalCostUSD)) · cumulative spend across active sessions")
            if let credits = d.snapshot.credits {
                if credits.unlimited == true {
                    lines.append("Credits: unlimited")
                } else if let balance = credits.balance {
                    lines.append(String(format: "Credits balance: $%.2f", balance))
                } else if credits.hasCredits {
                    lines.append("Credits: available")
                }
            }
        }
        if let reached = d.snapshot.reachedType, !reached.isEmpty {
            lines.append("⚠️ rate limit reached: \(reached)")
        }
        return lines.joined(separator: "\n")
    }

    private func quotaWindowLabel(_ minutes: Int) -> String {
        // Tolerate Codex v1's 299 / 10079 off-by-one quirk.
        switch minutes {
        case 299, 300: return "5h"
        case 10079, 10080: return "7d"
        default:
            if minutes >= 1440 { return "\(minutes / 1440)d" }
            if minutes >= 60 { return "\(minutes / 60)h" }
            return "\(minutes)m"
        }
    }

    private func barColor(_ pct: Double) -> Color {
        switch pct {
        case 90...: return IrrColors.pressureCritical
        case 80..<90: return IrrColors.pressureHigh
        case 60..<80: return IrrColors.pressureMedium
        default: return IrrColors.pressureLow
        }
    }

    private func formatClockTime(_ date: Date) -> String {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .short
        return f.string(from: date)
    }

    /// Compact reset label for the chip row. Same-day resets render as
    /// "HH:MM"; resets later in the week render as "EEE HH:MM" (e.g.
    /// "Fri 9:00"). Mirrors mockup 1's "resets 11:14" / "resets Fri 9:00".
    private func formatResetTime(_ date: Date) -> String {
        let cal = Calendar.current
        let now = Date()
        let f = DateFormatter()
        if cal.isDate(date, inSameDayAs: now) {
            f.dateStyle = .none
            f.timeStyle = .short
        } else {
            f.dateFormat = "EEE H:mm"
        }
        return f.string(from: date)
    }

    private func formatTimeUntil(_ date: Date) -> String {
        let seconds = max(0, Int(date.timeIntervalSinceNow))
        let h = seconds / 3600
        let m = (seconds % 3600) / 60
        if h >= 24 {
            let d = h / 24
            let rh = h % 24
            return rh == 0 ? "\(d)d" : "\(d)d \(rh)h"
        }
        if h > 0 { return "\(h)h \(m)m" }
        return "\(m)m"
    }

    private var sessionIconsView: some View {
        HStack(spacing: 2) {
            if sessionManager.sessions.isEmpty {
                Text("○")
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(.primary)
            } else if sessionManager.sessions.count <= 3 {
                ForEach(sessionManager.sessions.prefix(3)) { session in
                    Image(systemName: session.state.glyph)
                        .foregroundColor(session.state.color)
                        .font(.system(size: 12))
                }
            } else {
                Text("\(sessionManager.sessions.count) sessions")
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(.primary)
            }
        }
    }
    
    private var statusIndicator: some View {
        HStack(spacing: 4) {
            Circle()
                .fill(statusColor)
                .frame(width: 6, height: 6)
                .shadow(color: statusColor.opacity(0.5), radius: 3)
            Text(statusLabel)
                .font(.caption2)
                .foregroundColor(.secondary)
        }
        .tooltip(sessionManager.connectionState.tooltip)
    }

    private var statusColor: Color {
        switch sessionManager.connectionState {
        case .connected: return IrrColors.wsConnected
        case .connecting, .reconnecting: return IrrColors.wsConnecting
        case .disconnected: return IrrColors.wsDisconnected
        }
    }

    private var statusLabel: String {
        switch sessionManager.connectionState {
        case .connected: return "watching"
        case .connecting: return "connecting"
        case .reconnecting: return "reconnecting"
        case .disconnected: return "disconnected"
        }
    }
    
    // MARK: - Error View
    
    private func errorView(_ error: String) -> some View {
        // System .orange (not IrrColors.waiting) — this is generic warning
        // chrome, not the agent-waiting surface.
        HStack {
            Image(systemName: "exclamationmark.triangle")
                .foregroundColor(.orange)

            Text(error)
                .font(.caption)
                .foregroundColor(.secondary)
                .lineLimit(2)

            Spacer()
        }
        .padding(.horizontal, IrrSpacing.sp3)
        .padding(.vertical, 6)
        .background(Color.orange.opacity(0.1))
    }
}

// MARK: - Session Row View

struct ContextBar: View {
    let utilization: Double
    let pressureColor: Color
    var label: String? = nil

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: IrrRadius.xs)
                    .fill(IrrColors.trackFill)
                RoundedRectangle(cornerRadius: IrrRadius.xs)
                    .fill(pressureColor)
                    .frame(width: geo.size.width * min(CGFloat(utilization) / 100, 1.0))
                if let label {
                    Text(label)
                        .font(.system(size: 8, weight: .medium, design: .monospaced))
                        .foregroundColor(.secondary.opacity(0.8))
                        .padding(.trailing, 4)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
        }
    }
}

struct SessionRowView: View {
    let session: SessionState
    let agentNumber: Int
    var activeSubagentCount: Int = 0
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("displayMode") private var displayModeRaw: String = DisplayMode.context.rawValue
    @EnvironmentObject var sessionManager: SessionManager
    @State private var isHovered = false

    private var displayMode: DisplayMode { DisplayMode(rawValue: displayModeRaw) ?? .context }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                // State icon
                Image(systemName: session.state.glyph)
                    .font(.system(size: 10))
                    .foregroundColor(session.state.color)
                    .frame(width: 12)
                    .tooltip(session.state.label)
                    .accessibilityIdentifier("session-state-icon-\(session.id)")

                // Agent number or role emoji
                if let icon = session.roleIcon, !icon.isEmpty {
                    Text(icon)
                        .font(.system(size: 10))
                        .frame(width: 14, alignment: .center)
                        .tooltip(session.role?.capitalized ?? "")
                } else {
                    Text("\(agentNumber)")
                        .font(.system(size: 9, weight: .medium, design: .monospaced))
                        .foregroundColor(.secondary)
                        .frame(width: 12, alignment: .trailing)
                }

                // Active subagent count badge
                if activeSubagentCount > 0 {
                    Text("\(activeSubagentCount)")
                        .font(.system(size: 9, weight: .bold, design: .rounded))
                        .foregroundColor(.white)
                        .frame(width: 14, height: 14)
                        .background(IrrColors.working)
                        .clipShape(Circle())
                        .tooltip("\(activeSubagentCount) active subagent\(activeSubagentCount == 1 ? "" : "s")")
                }

                // Branch name — column shrinks when a subagent badge is present so
                // the context-bar column downstream starts at the same x on every row.
                // Badge occupies 14pt + 6pt spacing = 20pt, which is exactly the amount
                // we drop from the branch column here.
                Text(session.gitBranch ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.primary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .frame(width: activeSubagentCount > 0 ? 44 : 64, alignment: .leading)
                    .tooltip(session.gitBranch ?? "—")

                if displayMode == .context {
                    // Fixed-width columns: [bar+tokens_inside 100][cost 36 or % 32]
                    if let metrics = session.metrics, metrics.hasContextData {
                        ContextBar(utilization: metrics.contextUtilization,
                                   pressureColor: metrics.contextPressureColor,
                                   label: metrics.formattedTokenCount)
                            .frame(width: 100, height: 13)
                            .tooltip("Context window usage")
                        if showCostDisplay {
                            Text(metrics.formattedCost ?? "")
                                .font(.system(size: 9, weight: .medium, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 36, alignment: .leading)
                                .tooltip("Estimated session cost")
                        } else {
                            Text(metrics.formattedContextUtilization)
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundColor(metrics.contextPressureColor)
                                .frame(width: 32, alignment: .leading)
                        }
                    } else if let metrics = session.metrics, metrics.totalTokens > 0 {
                        // Tokens flowing but no context-window data — daemon
                        // sets metrics.contextWindowUnknown when the capacity
                        // manager has no LiteLLM pricing entry for the model
                        // (common for aider via LM Studio / any local
                        // provider). Render the raw token count in the bar
                        // slot so the row carries signal, and put cost (or
                        // a placeholder) in the secondary column.
                        Text(metrics.formattedTokenUsage)
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundColor(.secondary)
                            .frame(width: 100, height: 13, alignment: .leading)
                            .tooltip("Token count — context window not known for \(session.shortModelName)")
                        if showCostDisplay {
                            Text(metrics.formattedCost ?? "—")
                                .font(.system(size: 9, weight: .medium, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 36, alignment: .leading)
                        } else {
                            Text("—")
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 32, alignment: .leading)
                        }
                    } else {
                        Color.clear.frame(width: 132, height: 13)
                    }
                } else {
                    // Historical modes (1s/10s/60s): bar fills the same column as the
                    // Context bar+label so x-alignment stays stable across modes, and is
                    // taller because it carries no cost/% readout alongside it.
                    HistoryBarView(states: sessionManager.stateHistory[session.id] ?? [],
                                   bucketCount: sessionManager.historyBucketCount)
                        .frame(width: 132, height: 16)
                        .tooltip(displayMode.tooltip)
                }

                Spacer()

                if debugMode {
                    SessionActionButtons(session: session)
                }

                // Short model name + adapter icon — grouped so layoutPriority applies to both
                HStack(spacing: 6) {
                    Text(session.shortModelName)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .tooltip(session.effectiveModel)
                        .accessibilityIdentifier("session-model-label-\(session.id)")
                    if let icon = session.adapterIcon {
                        Image(nsImage: icon)
                            .frame(width: 12, height: 12)
                            .tooltip(session.adapterName)
                    }
                }
                .layoutPriority(1)
            }
            // Pin row to the tallest bar (history at 16pt) so toggling between
            // Context and 1s/10s/60s doesn't shift row height.
            .frame(minHeight: 16)

            // Waiting question block
            if session.state == .waiting,
               let text = session.metrics?.lastAssistantText, !text.isEmpty {
                Text(text)
                    .font(.system(size: 9))
                    .foregroundColor(IrrColors.waiting)
                    .lineLimit(3)
                    .truncationMode(.head)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 3)
                    .background(IrrColors.waitingDim)
                    .cornerRadius(IrrRadius.sm)
                    .padding(.top, 2)
                    // Surface the full prompt — head-truncation hides the start.
                    .tooltip(text)
            }

            // Context pressure alert (80%+, active sessions only)
            if let metrics = session.metrics,
               (session.state == .working || session.state == .waiting),
               metrics.contextUtilization >= 80 {
                let isCritical = metrics.contextUtilization >= 95
                let alertColor = isCritical ? IrrColors.pressureCritical : IrrColors.pressureHigh
                HStack(spacing: 4) {
                    Image(systemName: isCritical ? "exclamationmark.triangle.fill" : "exclamationmark.triangle")
                        .font(.system(size: 9))
                        .foregroundColor(alertColor)
                    Text("Switch to a fresh session soon")
                        .font(.system(size: 9))
                        .foregroundColor(alertColor)
                    Spacer()
                }
                .padding(.horizontal, 4)
                .padding(.vertical, 2)
                .background(alertColor.opacity(0.08))
                .cornerRadius(IrrRadius.sm)
                .padding(.top, 2)
                .tooltip(isCritical ? "Context window critically full" : "Context window nearing limit")
            }

            // Task list (Claude Code TaskCreate / TaskUpdate)
            if let tasks = session.metrics?.tasks, !tasks.isEmpty, !tasks.allSatisfy(\.isCompleted) {
                TaskListView(tasks: tasks)
            }

            // Debug info
            if debugMode {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    HStack(spacing: 8) {
                        Text(session.shortId)
                            .onTapGesture {
                                NSPasteboard.general.clearContents()
                                NSPasteboard.general.setString(session.id, forType: .string)
                            }
                            .tooltip("Click to copy full ID")
                        Text("updated: \(elapsedString(from: session.updatedAt, now: context.date))")
                        Text("created: \(elapsedString(from: session.firstSeen, now: context.date))")
                        if let metrics = session.metrics, metrics.totalTokens > 0 {
                            Text("ctx: \(metrics.formattedTokenUsage)")
                        }
                        Spacer()
                    }
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundColor(.secondary.opacity(0.7))
                    .padding(.top, 2)
                }
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 4)
        .background(isHovered ? IrrColors.surfaceHover : Color.clear)
        .contentShape(Rectangle())
        .onHover { hovering in
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                isHovered = hovering
            }
        }
        .onTapGesture {
            SessionLauncher.jump(session)
        }
        .accessibilityIdentifier("session-card-\(session.id)")
        .accessibilityLabel("\(session.projectName ?? "unknown") \(session.state.rawValue) \(session.shortModelName)")
        .accessibilityAddTraits(.isButton)
        .accessibilityHint("Brings the session's terminal or editor to the foreground")
    }

    private func elapsedString(from date: Date, now: Date) -> String {
        let total = max(0, Int(now.timeIntervalSince(date)))
        let h = total / 3600
        let m = (total % 3600) / 60
        let s = total % 60
        if h > 0 {
            return String(format: "%d:%02d:%02d", h, m, s)
        }
        return String(format: "%d:%02d", m, s)
    }
}

// MARK: - Task Progress

/// Wraps children left-to-right, starting a new row when the available width is exhausted.
private struct FlowLayout: Layout {
    var hSpacing: CGFloat = 4
    var vSpacing: CGFloat = 3

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            if x + size.width > maxWidth && x > 0 {
                y += rowHeight + vSpacing
                x = 0
                rowHeight = 0
            }
            x += size.width + hSpacing
            rowHeight = max(rowHeight, size.height)
        }
        return CGSize(width: maxWidth, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        // First pass: group subviews into rows so we know each row's
        // height before placing items. Second pass: place items with
        // their vertical center aligned to the row center, so tiny
        // circles and the taller "done/total" label line up.
        var rows: [[(sub: LayoutSubview, size: CGSize)]] = [[]]
        var currentRowWidth: CGFloat = 0
        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            let needsWrap = currentRowWidth + size.width > bounds.width && !rows[rows.count - 1].isEmpty
            if needsWrap {
                rows.append([])
                currentRowWidth = 0
            }
            rows[rows.count - 1].append((sub, size))
            currentRowWidth += size.width + hSpacing
        }

        var y = bounds.minY
        for row in rows {
            let rowHeight = row.map(\.size.height).max() ?? 0
            var x = bounds.minX
            for (sub, size) in row {
                let yCentered = y + (rowHeight - size.height) / 2
                sub.place(at: CGPoint(x: x, y: yCentered), proposal: .unspecified)
                x += size.width + hSpacing
            }
            y += rowHeight + vSpacing
        }
    }
}

/// Compact dot-progress row: one circle per task (filled = done, empty = pending) + "4 / 6" count.
/// Dots wrap to the next line when the row is full.
private struct TaskListView: View {
    let tasks: [SessionTask]

    var body: some View {
        let done = tasks.filter(\.isCompleted).count
        FlowLayout(hSpacing: 4, vSpacing: 3) {
            ForEach(tasks, id: \.id) { task in
                Group {
                    if task.isCompleted {
                        Circle().fill(IrrColors.ready.opacity(0.85))
                    } else {
                        Circle().strokeBorder(IrrColors.working, lineWidth: 1.5)
                    }
                }
                .frame(width: 7, height: 7)
                .tooltip(task.displayLabel)
            }
            Text("\(done) / \(tasks.count)")
                .font(.system(size: 9))
                .foregroundColor(.secondary)
                .padding(.leading, 2)
        }
    }
}

// MARK: - Session Action Buttons

struct SessionActionButtons: View {
    let session: SessionState
    @EnvironmentObject var sessionManager: SessionManager

    var body: some View {
        HStack(spacing: 4) {
            Button(action: {
                sessionManager.resetSessionState(sessionId: session.id)
            }) {
                Image(systemName: "arrow.counterclockwise")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .tooltip("Reset to ready state")

            Button(action: {
                sessionManager.deleteSession(sessionId: session.id)
            }) {
                Image(systemName: "trash")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .tooltip("Delete session")
        }
        .opacity(0.6)
    }
}

// MARK: - Subagent Row View

struct SubagentRowView: View {
    let session: SessionState
    @AppStorage("debugMode") private var debugMode: Bool = false
    @State private var isHovered = false

    var body: some View {
        HStack(spacing: 6) {
            // Indentation
            Spacer().frame(width: 24)

            // State indicator (small)
            Image(systemName: session.state.glyph)
                .font(.system(size: 9))
                .foregroundColor(session.state.color)
                .frame(width: 12)
                .tooltip(session.state.label)

            // Context utilization
            if let metrics = session.metrics, metrics.hasContextData {
                Text(metrics.formattedContextUtilization)
                    .font(.caption2)
                    .foregroundColor(metrics.contextPressureColor)
            } else if debugMode, let metrics = session.metrics, metrics.totalTokens > 0 {
                Text(metrics.formattedTokenUsage)
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            } else {
                Text("—")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            }

            Spacer()

            // Duration
            if let metrics = session.metrics {
                let isActive = session.state == .working || session.state == .waiting
                TimelineView(.periodic(from: .now, by: 1.0)) { timeline in
                    let _ = timeline.date
                    Text(isActive
                        ? metrics.formattedRealtimeElapsedTime(sessionFirstSeen: session.firstSeen)
                        : (metrics.elapsedSeconds > 0 ? metrics.formattedElapsedTime : "—"))
                        .font(.caption2)
                        .foregroundColor(.secondary.opacity(0.7))
                }
            } else {
                Text("—")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 3)
        .background(isHovered ? IrrColors.surfaceHoverSubtle : Color.clear)
        .onHover { hovering in
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("subagent-card-\(session.id)")
        .accessibilityLabel("subagent \(session.state.rawValue)")
    }
}

// MARK: - Recursive Group View (renders one API group with optional sub-groups)

struct GroupView: View {
    let group: SessionManager.AgentGroup
    var depth: Int = 0
    @EnvironmentObject var sessionManager: SessionManager
    @AppStorage("projectCostTimeframe") private var costTimeframeRaw: String = CostTimeframe.day.rawValue
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false

    // Source-of-truth for expansion lives on SessionManager so
    // SessionListView's height estimator can see it too.
    private var isExpanded: Bool {
        !sessionManager.collapsedGroupNames.contains(group.name)
    }

    private func toggleExpansion() {
        if sessionManager.collapsedGroupNames.contains(group.name) {
            sessionManager.collapsedGroupNames.remove(group.name)
        } else {
            sessionManager.collapsedGroupNames.insert(group.name)
        }
    }

    private var costTimeframe: CostTimeframe { CostTimeframe.from(costTimeframeRaw) }

    private var agentCount: Int {
        let direct = group.agents?.count ?? 0
        let nested = (group.groups ?? []).reduce(0) { $0 + ($1.agents?.count ?? 0) }
        return direct + nested
    }

    private var isTopLevel: Bool { depth == 0 }

    private var groupExpandTooltip: String {
        let action = isExpanded ? "Collapse" : "Expand"
        if isTopLevel && group.isGasTown {
            return "\(action) Gas Town group (external API calls)"
        }
        return "\(action) group"
    }

    /// Formatted cost for this group in the currently-selected time frame.
    /// Returns nil only when there is no cost data at all (hides the toggle).
    /// Returns "$0 / <frame>" when data exists for other frames but not this one,
    /// so the toggle remains visible and clickable.
    private var formattedCost: String? {
        guard showCostDisplay, isTopLevel, !group.isGasTown else { return nil }
        guard let costs = group.costs, !costs.isEmpty else { return nil }
        let v = costs[costTimeframe.rawValue] ?? 0
        guard v > 0 else { return "$0" + costTimeframe.suffix }
        let formatted: String
        if v < 0.01 { formatted = "<$0.01" }
        else if v >= 100 { formatted = String(format: "$%.0f", v) }
        else { formatted = String(format: "$%.2f", v) }
        return formatted + costTimeframe.suffix
    }

    private func cycleCostTimeframe() {
        costTimeframeRaw = costTimeframe.next().rawValue
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Button(action: {
                    // Instant toggle — any withAnimation here fights the
                    // popover's own resize timing and produces visible
                    // flicker on both expand and collapse.
                    toggleExpansion()
                }) {
                    HStack(spacing: 6) {
                        Image(systemName: isExpanded ? "chevron.down" : "chevron.right")
                            .font(.system(size: 8))
                            .foregroundColor(.secondary)
                            .frame(width: 10)

                        if isTopLevel && group.isGasTown {
                            Text("\u{26FD}")
                                .font(.system(size: 10))
                        }

                        Text(group.name)
                            .font(.system(isTopLevel ? .caption : .caption2, design: .monospaced))
                            .fontWeight(isTopLevel ? .semibold : .medium)
                            .foregroundColor(isTopLevel ? .primary : .secondary)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .tooltip(groupExpandTooltip)

                if let cost = formattedCost {
                    Button(action: cycleCostTimeframe) {
                        Text(cost)
                            .font(.system(size: 9, weight: .medium, design: .monospaced))
                            .foregroundColor(.secondary)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .tooltip("Click to cycle time frame (day → week → month → year)")
                }

                Spacer()

                let count = isTopLevel ? agentCount : (group.agents?.count ?? 0)
                Text(isTopLevel ? "\(count) \(count == 1 ? "session" : "sessions")" : "\(count)")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(isTopLevel ? 0.7 : 0.5))

                if isTopLevel, sessionManager.apiGroups.count > 1,
                   let idx = sessionManager.apiGroups.firstIndex(where: { $0.name == group.name }) {
                    HStack(spacing: 0) {
                        reorderButton(icon: "chevron.up", tooltip: "Move group up", disabled: idx == 0) {
                            sessionManager.moveProjectGroupUp(name: group.name)
                        }
                        reorderButton(icon: "chevron.down", tooltip: "Move group down", disabled: idx == sessionManager.apiGroups.count - 1) {
                            sessionManager.moveProjectGroupDown(name: group.name)
                        }
                    }
                }
            }
            .padding(.horizontal, isTopLevel ? 12 : 20)
            .padding(.vertical, isTopLevel ? 4 : 3)

            if isExpanded {
                ForEach(Array((group.agents ?? []).enumerated()), id: \.element.id) { index, session in
                    SessionRowView(
                        session: session,
                        agentNumber: index + 1,
                        activeSubagentCount: session.activeSubagentCount
                    )
                    .padding(.leading, isTopLevel ? 8 : 16)
                }

                ForEach(group.groups ?? [], id: \.name) { subGroup in
                    GroupView(group: subGroup, depth: depth + 1)
                }
            }
        }
    }

    private func reorderButton(icon: String, tooltip: String, disabled: Bool, action: @escaping () -> Void) -> some View {
        Button {
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) { action() }
        } label: {
            Image(systemName: icon)
                .font(.system(size: 10))
                .foregroundColor(.secondary)
                .frame(width: 14, height: 20)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.3 : 1.0)
        .tooltip(tooltip)
    }
}

// MARK: - Cost Timeframe

enum CostTimeframe: String, CaseIterable {
    case day, week, month, year

    var suffix: String {
        switch self {
        case .day:   return " / day"
        case .week:  return " / week"
        case .month: return " / month"
        case .year:  return " / year"
        }
    }

    static func from(_ raw: String) -> CostTimeframe {
        CostTimeframe(rawValue: raw) ?? .day
    }

    func next() -> CostTimeframe {
        let all = Self.allCases
        let idx = all.firstIndex(of: self) ?? 0
        return all[(idx + 1) % all.count]
    }
}

// MARK: - Preview

struct SessionListView_Previews: PreviewProvider {
    static var previews: some View {
        SessionListView()
            .environmentObject(GasTownProvider())
            .environmentObject({
                let manager = SessionManager()
                manager.sessions = [
                    SessionState(
                        id: "sess_abc123def456",
                        state: .working,
                        model: "claude-sonnet-4-6",
                        cwd: "/Users/user/projects/multi-cc-bar",
                        gitBranch: "main",
                        projectName: "multi-cc-bar",
                        firstSeen: Date().addingTimeInterval(-180),
                        updatedAt: Date().addingTimeInterval(-60),
                        eventCount: 5,
                        lastEvent: "UserPromptSubmit",
                        metrics: SessionMetrics(
                            elapsedSeconds: 180,
                            totalTokens: 15000,
                            modelName: "claude-sonnet-4-6",
                            contextWindow: 200000,
                            contextUtilization: 7.5,
                            pressureLevel: "safe",
                            contextWindowUnknown: nil,
                            estimatedCostUSD: nil,
                            lastAssistantText: nil,
                            tasks: nil
                        )
                    )
                ]
                return manager
            }())
    }
}