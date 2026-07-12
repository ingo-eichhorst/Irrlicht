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

    /// SF Symbol for the compact header toggle: a gauge for context
    /// utilization, a clock for the time-windowed activity views.
    var icon: String { self == .context ? "gauge.medium" : "clock" }

    /// Window length shown beside the clock icon ("" for context).
    var compactMinutes: String {
        switch self {
        case .context:    return ""
        case .history1s:  return "1"
        case .history10s: return "10"
        case .history60s: return "60"
        }
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
    @EnvironmentObject var updateManager: UpdateManager
    @State private var isQuitButtonHovered = false
    @State private var isSettingsButtonHovered = false
    @State private var isUpdatesButtonHovered = false
    @State private var isHistoryButtonHovered = false
    @State private var showSettings = false
    /// Footer "History" swaps the panel body to the cost-analytics view (#755).
    @State private var showHistory = false
    /// Settings → "Review agent permissions…" swaps the panel body to the
    /// wizard in review mode (issue #570).
    @State private var showPermissionsReview = false
    /// Non-nil while the auto consent wizard is presented: the agent set
    /// LOCKED at presentation time. Locking means a mid-decision detection
    /// flip (agent process exits) can't tear the wizard down, and a newly
    /// detected agent can't inject default-on rows into an open wizard —
    /// it gets its own prompt once this one resolves. reconcileAutoWizard
    /// owns the lifecycle.
    @State private var autoWizardAgents: [String]?
    /// "Decide Later" remembers which agent set was dismissed so the auto
    /// wizard doesn't bounce right back; a different agent appearing (new
    /// signature) re-triggers it.
    @State private var dismissedWizardSignature: String?
    @AppStorage("displayMode") private var displayModeRaw: String = DisplayMode.context.rawValue
    @AppStorage("showQuotaForecast") private var showQuotaForecast: Bool = true
    // Shared timeframe for all provider usage chips; clicking any chip cycles
    // it. Independent of the project-cost timeframe (`projectCostTimeframe`).
    @AppStorage("usageCostTimeframe") private var usageCostTimeframeRaw: String = CostTimeframe.day.rawValue

    private var displayMode: DisplayMode { DisplayMode(rawValue: displayModeRaw) ?? .context }
    private var usageCostTimeframe: CostTimeframe { .from(usageCostTimeframeRaw) }
    private func cycleUsageTimeframe() { usageCostTimeframeRaw = usageCostTimeframe.next().rawValue }

    /// Stable identity of the current pending-wizard agent set, for the
    /// "Decide Later" suppression above.
    private var wizardSignature: String {
        sessionManager.pendingWizardAgents.map(\.name).sorted().joined(separator: ",")
    }

    /// Reconciles auto-wizard visibility with the consent snapshot (#570).
    /// Presentation: a detected agent has pending permissions and the set
    /// wasn't just dismissed. Dismissal: every LOCKED agent has no pending
    /// permissions left — i.e. answered, here or on the web dashboard
    /// (first answer wins). A detection flip alone (agent exited while the
    /// user is deciding) never dismisses an open wizard.
    private func reconcileAutoWizard() {
        let snapAgents = sessionManager.permissionsSnapshot?.agents ?? []
        if let locked = autoWizardAgents {
            let stillPending = locked.contains { name in
                snapAgents.first(where: { $0.name == name })?
                    .permissions.contains { $0.state == .pending } ?? false
            }
            if !stillPending {
                autoWizardAgents = nil
            }
        }
        if autoWizardAgents == nil {
            let names = sessionManager.pendingWizardAgents.map(\.name).sorted()
            if !names.isEmpty && dismissedWizardSignature != names.joined(separator: ",") {
                autoWizardAgents = names
            }
        }
    }

    var body: some View {
        Group {
            if showSettings {
                SettingsView(
                    isPresented: $showSettings,
                    showPermissionsReview: $showPermissionsReview,
                    sessionManager: sessionManager
                )
            } else if showHistory {
                HistoryView(onClose: { showHistory = false })
            } else if showPermissionsReview {
                PermissionWizardView(mode: .review, onClose: { showPermissionsReview = false })
            } else if let locked = autoWizardAgents {
                PermissionWizardView(
                    mode: .auto,
                    lockedAgents: locked,
                    onClose: {
                        // "Decide Later": suppress until the pending set
                        // changes (or next app launch — consent stays
                        // pending daemon-side).
                        dismissedWizardSignature = wizardSignature
                        autoWizardAgents = nil
                    }
                )
            } else {
                mainPanel
            }
        }
        .onAppear { reconcileAutoWizard() }
        .onChange(of: sessionManager.permissionsSnapshot) { _ in reconcileAutoWizard() }
    }

    private var mainPanel: some View {
        Group {
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
                    Button(action: { showHistory = true }) {
                        Text("History")
                            .foregroundColor(.secondary)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(isHistoryButtonHovered ? IrrColors.surfaceHover : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isHistoryButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)
                    .tooltip("View historical cost analytics")

                    Divider().frame(height: 20)

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

                    Button(action: { updateManager.checkForUpdates() }) {
                        Text("Updates\u{2026}")
                            .foregroundColor(.secondary)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(isUpdatesButtonHovered ? IrrColors.surfaceHover : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isUpdatesButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)
                    .tooltip("Check for app updates")

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
                HStack(spacing: 1) {
                    Image(systemName: displayMode.icon)
                    if displayMode.isHistory {
                        Text(displayMode.compactMinutes)
                            .font(.system(size: 9, weight: .semibold, design: .monospaced))
                    }
                }
                .font(.system(size: 11))
                .foregroundColor(displayMode.isHistory ? IrrColors.working : .secondary)
                .frame(minWidth: 16)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .tooltip(displayMode.tooltip)
            .id("mode-cycle-btn")

            summaryCollapseAllButton

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

    /// Header control that toggles the global collapse state for every
    /// session's task-summary block at once (issue #738).
    private var summaryCollapseAllButton: some View {
        let collapsed = sessionManager.summariesCollapsed
        return Button {
            sessionManager.summariesCollapsed.toggle()
        } label: {
            Image(systemName: collapsed ? "rectangle.expand.vertical" : "rectangle.compress.vertical")
                .font(.system(size: 11))
                .foregroundColor(.secondary)
                .frame(width: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .tooltip(collapsed ? "Expand all task summaries" : "Collapse all task summaries")
        .accessibilityIdentifier("summary-collapse-all")
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
            // User opted out of the quota chip in Settings — show the
            // app name so the header isn't a bare em-dash. Version is
            // still tucked into the Settings footer per the maintainer's
            // request from #309.
            Text("Irrlicht")
                .font(.headline)
                .foregroundColor(.primary)
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
        let isStale: Bool
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
        /// True when any window's resetsAt is at or before now — the
        /// snapshot describes a window the provider has rolled over
        /// without us seeing a fresh tick. Surfaced via dimmer chip
        /// rendering + tooltip note rather than dropped entirely, so
        /// users see the last-known state instead of an empty header.
        var isStale: Bool

        func toWidgetData(id: String) -> QuotaWidgetData {
            QuotaWidgetData(
                id: id,
                mode: mode,
                snapshot: snapshot,
                session: session,
                imminent: imminent,
                totalCostUSD: totalCostUSD,
                isStale: isStale
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
    /// `totalCostUSD` sums cumulative `estimatedCostUSD` across every
    /// matching session; it now feeds only the tooltip / accessibility
    /// label ("cumulative spend across active sessions"). The usage chip
    /// itself renders the daemon's windowed per-provider rollup
    /// (`SessionManager.providerCosts`), not this cumulative figure.
    private func mergeIntoBuckets(session: SessionState, into buckets: inout [String: ChipBucket]) {
        guard let snap = session.metrics?.rateLimit else { return }
        let now = Date()
        let snapIsStale = snap.windows.contains(where: { $0.resetsAt <= now })
        let key = snap.providerKey(adapter: session.adapter)
            ?? "unknown:\(session.adapter ?? "")"
        let mode = resolveChipMode(snap: snap, providerKey: key)
        let imminent = snap.imminentWindow
        let sessionCost = session.metrics?.estimatedCostUSD ?? 0
        if var existing = buckets[key] {
            // Subscription wins over usage when both paths are seen
            // (rare — one OAuth account on both subscription and API
            // key): the bars are the richer signal. Trade-off: this
            // can replace a fresh usage entry with a stale subscription
            // one, losing recency in service of richer rendering. The
            // mixed-mode case is uncommon enough that we accept it
            // rather than introduce a "prefer fresh except when…"
            // tiebreaker.
            if existing.mode == .usage && mode == .subscription {
                existing.mode = .subscription
                existing.snapshot = snap
                existing.session = session
                existing.imminent = imminent
                existing.isStale = snapIsStale
            } else if existing.isStale && !snapIsStale {
                // Always prefer a fresh snapshot over a stale one,
                // regardless of which has the higher sampledAt — a
                // pre-rollover snapshot can be "newer" by timestamp
                // but still describe a window that already reset.
                existing.snapshot = snap
                existing.session = session
                existing.imminent = imminent
                existing.isStale = false
            } else if existing.isStale == snapIsStale && snap.sampledAt > existing.snapshot.sampledAt {
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
                mode: mode,
                isStale: snapIsStale
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
    ///   - usage        → provider icon + windowed spend, click-to-cycle (mockup 2)
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
                if d.snapshot.windows.isEmpty {
                    // Subscription forced (or auto-detected) but the snapshot
                    // carries no rate-limit windows. Symmetric with the usage
                    // zero-state: a short phrase rather than an empty chip.
                    Text("no subscription data")
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundColor(.secondary)
                        .frame(minWidth: 88, alignment: .leading)
                } else {
                    VStack(alignment: .leading, spacing: 1) {
                        ForEach(d.snapshot.windows, id: \.windowMinutes) { window in
                            quotaWindowRow(window, compact: compact)
                        }
                    }
                }
            case .usage:
                quotaUsageBody(d, compact: compact)
            }
        }
        // Stale snapshots render at half opacity so the user can tell
        // the data pre-dates the current window — the values are still
        // visible (better than an empty header) but visibly muted, and
        // the tooltip names the staleness explicitly.
        .opacity(d.isStale ? 0.5 : 1.0)
        .tooltip(quotaTooltip(d))
    }

    /// Usage-mode chip body — windowed spend on this provider for the
    /// currently selected timeframe, with a "spend" subtitle. Click to
    /// cycle the timeframe (day → week → month → year), mirroring the
    /// project-group cost text; the choice is shared across all usage
    /// chips via `usageCostTimeframe`. Always 2 lines so the chip has the
    /// same height as the subscription variant (5h + 7d rows) and the two
    /// render cleanly side-by-side in the header.
    ///
    /// Sourced from the daemon's per-provider cost rollup (`provider_costs`),
    /// keyed by the chip's providerKey (`d.id`). A project can mix providers,
    /// so this can't be re-derived from group costs client-side. When a
    /// provider has no spend in the window we render `$0 / <frame>` — a
    /// windowed zero is honest (matches the project cost display), so there's
    /// no separate em-dash zero-state.
    ///
    /// A `minWidth: 88` matches the subscription chip's row width
    /// (label + 40pt bar + percent) so a usage chip with a short
    /// headline doesn't collapse to an icon-only sliver next to a
    /// bars chip.
    @ViewBuilder
    private func quotaUsageBody(_ d: QuotaWidgetData, compact _: Bool) -> some View {
        let spend = sessionManager.providerCosts[d.id]?[usageCostTimeframe.rawValue] ?? 0
        Button(action: cycleUsageTimeframe) {
            VStack(alignment: .leading, spacing: 1) {
                Text(formatUsageCost(spend) + usageCostTimeframe.suffix)
                    .font(.system(size: 10, weight: .semibold, design: .monospaced))
                    .foregroundColor(.primary)
                Text("spend")
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundColor(.secondary)
            }
            .frame(minWidth: 88, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .tooltip("Click to cycle time frame (day → week → month → year)")
    }

    /// Format the cost headline: zero renders `$0`, tiny costs render
    /// `<$0.01`, normal costs render with two decimals, ≥ $100 drops to
    /// integer dollars to keep the chip from growing. Matches GroupView's
    /// project-cost formatting so the suffix (` / day`, …) reads the same.
    private func formatUsageCost(_ cost: Double) -> String {
        if cost <= 0 { return "$0" }
        if cost < 0.01 { return "<$0.01" }
        if cost >= 100 { return String(format: "$%.0f", cost) }
        return String(format: "$%.2f", cost)
    }

    /// One row inside a chip. In compact mode (multiple chips visible)
    /// the inline reset time is dropped — it lives in the tooltip — and
    /// the bar shrinks so two or three chips fit in the 380pt header.
    @ViewBuilder
    private func quotaWindowRow(_ w: RateLimitWindowInfo, compact: Bool) -> some View {
        // Compute once per row — SwiftUI re-invokes view bodies on every
        // SessionManager publish, and calling quotaPacePercent twice
        // would also let two Date() captures disagree by microseconds.
        let pace = Self.quotaPacePercent(w)
        HStack(spacing: 6) {
            Text(quotaWindowLabel(w.windowMinutes))
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.secondary)
                .frame(width: 14, alignment: .leading)

            quotaBar(percent: w.usedPercent,
                     color: Self.barColor(used: w.usedPercent, pace: pace),
                     pacePercent: pace)
                .frame(width: compact ? 60 : 70, height: 5)

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

    /// "Expected percent used if you've been pacing evenly through the
    /// window so far." Anchored to current wall-clock time, not the
    /// snapshot's `sampledAt` — the marker should always reflect where
    /// the user *should be* right now, independent of when the
    /// snapshot was last refreshed.
    ///
    /// Returns nil when the window can't be paced: zero-duration or
    /// missing `resetsAt`. A `resetsAt` already in the past produces a
    /// clamped 100% (marker pinned to the right edge) which combined
    /// with the chip's stale-opacity tint reads naturally as "this
    /// snapshot pre-dates the current window."
    ///
    /// Codex's v1 minute-window quirk (`299` instead of `300`,
    /// `10079` instead of `10080`) is left as-is here — the
    /// ≤60-second drift in the implied window-start time is well
    /// below the resolution the marker conveys visually.
    /// `static` + non-private (like `barColor`) so `QuotaMenuBarRenderer`'s
    /// menu-bar icon can share this exact implementation instead of
    /// re-deriving the same math in a second place.
    static func quotaPacePercent(_ w: RateLimitWindowInfo) -> Double? {
        guard w.windowMinutes > 0 else { return nil }
        guard w.resetsAt.timeIntervalSince1970 > 0 else { return nil }
        let windowSeconds = Double(w.windowMinutes) * 60
        let windowStart = w.resetsAt.addingTimeInterval(-windowSeconds)
        let elapsed = Date().timeIntervalSince(windowStart)
        let pct = (elapsed / windowSeconds) * 100.0
        return min(100, max(0, pct))
    }

    /// A rounded-rect progress bar with an optional vertical pace
    /// marker (thin red line at `pacePercent`). The marker reads
    /// "where you should be if you've been pacing evenly" — fill past
    /// the marker means burning quota faster than the window's linear
    /// rate; fill behind it means headroom. ZStack so the fill, track,
    /// and marker render with the same corner radius without clipping
    /// artifacts at small sizes.
    @ViewBuilder
    private func quotaBar(percent: Double, color: Color, pacePercent: Double?) -> some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(Color.secondary.opacity(0.2))
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(color)
                    .frame(width: geo.size.width * min(1.0, max(0.0, percent / 100.0)))
                if let pace = pacePercent, (0...100).contains(pace) {
                    Rectangle()
                        .fill(Color.red)
                        .frame(width: 1)
                        .offset(x: geo.size.width * (pace / 100.0) - 0.5)
                }
            }
        }
    }

    private func quotaTooltip(_ d: QuotaWidgetData) -> String {
        var lines: [String] = []
        if let plan = d.snapshot.planTypeLabel {
            lines.append(plan)
        }
        if d.isStale {
            lines.append("⚠️ snapshot pre-dates current window — waiting for next statusline tick")
        }
        switch d.mode {
        case .subscription:
            for w in d.snapshot.windows {
                // Provider data is integer-precision (any decimals are
                // floating-point noise from a JSON marshal/unmarshal
                // round-trip on the daemon side, e.g. 7.000000000000001
                // for a value the provider reported as 7). Render as
                // whole percent so the tooltip matches the chip body.
                //
                // Line template:
                //   <window>: <used>% used · <pace verdict> · resets in <when>
                //
                // The earlier shape included an explicit "pace 42%
                // (behind by 26pt)" suffix that grew long enough to
                // wrap mid-parenthesis at the macOS tooltip width.
                // The pace percent is redundant — the delta is the
                // load-bearing signal — so we drop it and put the
                // verdict inline, no parentheses.
                let used = Int(w.usedPercent.rounded())
                let label = quotaWindowLabel(w.windowMinutes)
                let resets = formatTimeUntil(w.resetsAt)
                var line = "\(label): \(used)% used · resets in \(resets)"
                if let pace = Self.quotaPacePercent(w) {
                    let delta = used - Int(pace.rounded())
                    let verdict: String
                    if delta > 0 { verdict = "\(delta)pt over pace" }
                    else if delta < 0 { verdict = "\(-delta)pt under pace" }
                    else { verdict = "on pace" }
                    line = "\(label): \(used)% used · \(verdict) · resets in \(resets)"
                }
                lines.append(line)
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

    /// Pace-aware bar tint. The ramp is driven by how far the fill is
    /// *ahead of* the steady-state pace marker rather than by raw
    /// `used_percent` alone — burning 60% in the first hour of a 5h
    /// window is more alarming than burning 60% in the fourth hour.
    ///
    /// Red is intentionally absent from the fill palette so it doesn't
    /// blur into the red pace marker: high-severity states use orange.
    /// Note: the `IrrColors.pressure*` tokens are misnamed —
    /// `pressureMedium` is system orange and `pressureHigh` is system
    /// red — so we bypass them and use SwiftUI's `.yellow` / `.orange`
    /// directly for unambiguous intent.
    ///
    /// Rules:
    ///   - `used - pace` ≥ 15pt, or `used` ≥ 85% → orange: you're
    ///     burning fast enough to blow the window before reset, or
    ///     the cap is imminent regardless of pace.
    ///   - `used - pace` ≥ 5pt                   → yellow: noticeable
    ///     overshoot but recoverable.
    ///   - otherwise                              → green: on pace or
    ///     behind, plenty of headroom.
    ///
    /// When `pace` is nil (no expiry data on the window) we fall back
    /// to a purely absolute ramp so the chip still has a sensible
    /// color in that edge case.
    ///
    /// Named thresholds for the bar-color ramp. Centralised so tuning
    /// the boundaries doesn't require hunting through the function
    /// body — and so the XCTest reads them by name when it asserts
    /// the boundary behaviour.
    enum QuotaBarThreshold {
        /// Absolute used-percent at which the bar flips to orange
        /// regardless of pace — the cap itself is imminent.
        static let absoluteOrange: Double = 85
        /// `used - pace` overshoot at which the bar flips to orange.
        static let paceDeltaOrange: Double = 15
        /// `used - pace` overshoot at which the bar flips to yellow.
        static let paceDeltaYellow: Double = 5
        /// Absolute used-percent thresholds used as a fallback when
        /// the window has no `resetsAt` and we can't compute a pace.
        static let fallbackOrange: Double = 70
        static let fallbackYellow: Double = 50
    }

    /// Three-way verdict the pace-aware ramp above resolves to, decoupled
    /// from any particular color representation so both the SwiftUI chip
    /// (`barColor`, a `Color`) and the menu-bar icon (`QuotaMenuBarRenderer`,
    /// a bare hex string for SVG) can share the one branching implementation
    /// instead of keeping two hand-synced copies of the same thresholds.
    enum QuotaColorTier {
        case green, yellow, orange
    }

    /// `static` + non-private so XCTests can table-drive the threshold
    /// boundaries without instantiating the view, and so QuotaMenuBarRenderer
    /// can call it directly.
    static func quotaColorTier(used: Double, pace: Double?) -> QuotaColorTier {
        if used >= QuotaBarThreshold.absoluteOrange { return .orange }
        guard let pace = pace else {
            switch used {
            case QuotaBarThreshold.fallbackOrange...: return .orange
            case QuotaBarThreshold.fallbackYellow...: return .yellow
            default: return .green
            }
        }
        let delta = used - pace
        if delta >= QuotaBarThreshold.paceDeltaOrange { return .orange }
        if delta >= QuotaBarThreshold.paceDeltaYellow { return .yellow }
        return .green
    }

    static func barColor(used: Double, pace: Double?) -> Color {
        switch quotaColorTier(used: used, pace: pace) {
        case .green: return IrrColors.pressureLow
        case .yellow: return .yellow
        case .orange: return .orange
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
                ForEach(sessionManager.sessions.prefix(3), id: \.rowID) { session in
                    SessionStateIcon(state: session.state, size: 12)
                }
            } else {
                Text("\(sessionManager.sessions.count) sessions")
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(.primary)
            }
        }
    }
    
    private var statusIndicator: some View {
        Circle()
            .fill(statusColor)
            .frame(width: 6, height: 6)
            .shadow(color: statusColor.opacity(0.5), radius: 3)
            // Multi-source: one line per source — "Local — connected" and, for
            // a relay, one line per connected daemon by label.
            .tooltip(sessionManager.connectionTooltip)
            // The dot is the only visible affordance now; VoiceOver
            // needs an explicit label since "Circle" alone tells the
            // user nothing about connection state.
            .accessibilityLabel("Connection: \(sessionManager.connectionTooltip)")
    }

    private var statusColor: Color {
        let aggregate = sessionManager.aggregateConnectionState
        // A stalled reconnect — local (#843) or relay (#846) — is a more
        // severe signal than an ordinary transient "reconnecting" blip —
        // flag it red rather than yellow even though the auto-retry loop is
        // still quietly running. Only when it isn't masked by another
        // healthy source, though — the other connection can be carrying the
        // session list just fine while this one is stuck, and the aggregate
        // already reports that (`.connected` wins in `aggregateConnectionState`).
        if aggregate != .connected {
            if sessionManager.useLocalDaemon && sessionManager.localConnectionStalled {
                return IrrColors.wsDisconnected
            }
            if sessionManager.useRelayServer && !sessionManager.relayServerURL.isEmpty && sessionManager.relayConnectionStalled {
                return IrrColors.wsDisconnected
            }
        }
        switch aggregate {
        case .connected: return IrrColors.wsConnected
        case .connecting, .reconnecting: return IrrColors.wsConnecting
        case .disconnected: return IrrColors.wsDisconnected
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
                        cwd: "/Users/user/projects/multi-cc-bar",  // NOSONAR (swift:S1075) — SwiftUI preview fixture, never runs in production
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