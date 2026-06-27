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
    private func quotaUsageBody(_ d: QuotaWidgetData, compact: Bool) -> some View {
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
        let pace = quotaPacePercent(w)
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
    private func quotaPacePercent(_ w: RateLimitWindowInfo) -> Double? {
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
                if let pace = quotaPacePercent(w) {
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

    /// `static` + non-private so XCTests can table-drive the threshold
    /// boundaries without instantiating the view.
    static func barColor(used: Double, pace: Double?) -> Color {
        if used >= QuotaBarThreshold.absoluteOrange { return .orange }
        guard let pace = pace else {
            switch used {
            case QuotaBarThreshold.fallbackOrange...: return .orange
            case QuotaBarThreshold.fallbackYellow...: return .yellow
            default: return IrrColors.pressureLow
            }
        }
        let delta = used - pace
        if delta >= QuotaBarThreshold.paceDeltaOrange { return .orange }
        if delta >= QuotaBarThreshold.paceDeltaYellow { return .yellow }
        return IrrColors.pressureLow
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
        switch sessionManager.aggregateConnectionState {
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
    @AppStorage("userIntentDisplay") private var userIntentDisplay: Bool = false
    @AppStorage(ContextPressureThreshold.valueKey) private var contextThresholdValue: Double = ContextPressureThreshold.defaultValue
    @AppStorage(ContextPressureThreshold.unitKey) private var contextThresholdUnitRaw: String = ContextPressureThreshold.defaultUnit.rawValue
    @EnvironmentObject var sessionManager: SessionManager
    @State private var isHovered = false

    private var displayMode: DisplayMode { DisplayMode(rawValue: displayModeRaw) ?? .context }

    private var contextThreshold: ContextPressureThreshold {
        ContextPressureThreshold(
            value: contextThresholdValue > 0 ? contextThresholdValue : ContextPressureThreshold.defaultValue,
            unit: ContextPressureThreshold.Unit(rawValue: contextThresholdUnitRaw) ?? ContextPressureThreshold.defaultUnit
        )
    }

    private var summaryCollapsed: Bool {
        sessionManager.summariesCollapsed
    }

    /// Session detail block. While waiting, shows the agent's pending question
    /// (orange) — the waiting state is already clear from the row's state icon,
    /// so there's no separate label. When the beta "User-Intent Display" setting
    /// is on, also shows the task summary — the agent's irrlicht-summary marker,
    /// else its first user prompt (issue #738) — as a purple "what the user
    /// asked for" block. Collapse is driven globally by the header collapse-all
    /// control; there is no per-row toggle.
    @ViewBuilder
    private var summaryBlock: some View {
        let summary = session.metrics?.taskSummary
        let question = session.state == .waiting ? session.metrics?.lastAssistantText : nil
        let collapsed = summaryCollapsed
        let showIntent = userIntentDisplay && (summary?.isEmpty == false) && !collapsed
        let showQuestion = (question?.isEmpty == false) && !collapsed
        if showIntent || showQuestion {
            VStack(alignment: .leading, spacing: 2) {
                // User intent (beta): what the user asked for.
                if showIntent, let s = summary {
                    Text(s)
                        .font(.system(size: 9))
                        .foregroundColor(IrrColors.intent)
                        .lineLimit(3)
                        .truncationMode(.tail)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 3)
                        .background(IrrColors.intentDim)
                        .cornerRadius(IrrRadius.sm)
                        .tooltip(s)
                }

                // Pending question: what the agent is asking.
                if showQuestion, let q = question {
                    Text(q)
                        .font(.system(size: 9))
                        .foregroundColor(IrrColors.waiting)
                        .lineLimit(3)
                        .truncationMode(.head)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 3)
                        .background(IrrColors.waitingDim)
                        .cornerRadius(IrrRadius.sm)
                        // Surface the full prompt — head-truncation hides the start.
                        .tooltip(q)
                }
            }
            .padding(.top, 2)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                // State icon
                SessionStateIcon(state: session.state, size: 12)
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

                // Worker name / bead-ID for Gas Town rows (parity with the web
                // worker chips). Gated on `role` so non-orchestrator rows are
                // pixel-identical; bounded width so the branch column downstream
                // doesn't shift across orchestrator rows.
                if session.role != nil {
                    if let wn = session.workerName, !wn.isEmpty {
                        Text(wn)
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.primary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .frame(maxWidth: 60, alignment: .leading)
                    }
                    if let wid = session.workerID, !wid.isEmpty {
                        Text(String(wid.prefix(8)))
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                            .tooltip(wid)
                    }
                }

                // Origin glyph (#538) — a cloud marks a session delivered by a
                // remote relay daemon; local sessions show nothing, so a
                // local-only dashboard is visually unchanged. A session that is
                // also present locally is filtered to the local row upstream
                // (relayOnly), so any row with a daemonID is genuinely remote.
                // Tooltip = the daemon's hostname (from the relay's label map).
                if let daemonID = session.daemonID {
                    let host = sessionManager.relayDaemons[daemonID]
                        ?? sessionManager.offlineDaemons[daemonID] ?? daemonID
                    let offline = sessionManager.isOffline(session)
                    Image(systemName: offline ? "cloud.slash" : "cloud")
                        .font(.system(size: 9))
                        .foregroundColor(.secondary)
                        .frame(width: 14, alignment: .center)
                        .tooltip(offline ? "\(host) — offline" : host)
                }

                // Cache-creation regression glyph (#374) — an upward arrow marks
                // a session whose median cache-creation per turn has regressed
                // past the project baseline. The tooltip names the regressing
                // upstream version when the daemon could attribute it.
                if session.metrics?.cacheBloat == true {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.system(size: 9))
                        .foregroundColor(.orange)
                        .frame(width: 14, alignment: .center)
                        .tooltip(session.metrics?.cacheBloatTooltip?.isEmpty == false
                            ? session.metrics!.cacheBloatTooltip!
                            : "cache-creation regression")
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

                // Background-agent badge (#744) — a moon marks a Claude Code Agent
                // View background agent running detached in the daemon pool after
                // its window closed. Amber "zzz" moon when no window owns it
                // (detached); a muted moon when a window is still open.
                if let bg = session.background {
                    let detached = bg.detached ?? false
                    let trimmedName = (bg.name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                    let label = trimmedName.isEmpty ? "" : " (\(trimmedName))"
                    Image(systemName: detached ? "moon.zzz.fill" : "moon.fill")
                        .font(.system(size: 9))
                        .foregroundColor(detached ? IrrColors.waiting : .secondary)
                        .frame(width: 14, height: 14)
                        .tooltip(detached
                            ? "Detached background agent\(label) — no open window; runs in the Claude Code daemon pool"
                            : "Background agent\(label)")
                }

                // Branch name — column shrinks by one badge's width (14pt + 6pt
                // spacing = 20pt) for EACH leading badge present (subagent count
                // and/or background-agent), so the context-bar column downstream
                // starts at the same x on every row regardless of how many badges
                // a row carries.
                Text(session.gitBranch ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.primary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .frame(width: 64 - CGFloat(20 * ((activeSubagentCount > 0 ? 1 : 0) + (session.background != nil ? 1 : 0))), alignment: .leading)
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

                // Backchannel control affordance (#724): only when the daemon
                // says this session is controllable (toggle on + consent + a
                // usable terminal backend).
                if session.controllable == true {
                    SessionControlButton(session: session)
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

            // Task summary + waiting question — a single collapsible block
            // (issue #738). The summary ("what is this session about") shows
            // in any state; the question shows only while waiting. The list
            // header's collapse-all toggle governs every row at once (global
            // state, no per-row chevron). Default expanded so the info is visible.
            summaryBlock

            // Context pressure alert (configurable threshold, active sessions only — #689)
            if let metrics = session.metrics,
               (session.state == .working || session.state == .waiting),
               contextThreshold.isExceeded(by: metrics) {
                let alertColor = IrrColors.pressureHigh
                HStack(spacing: 4) {
                    Image(systemName: "exclamationmark.triangle")
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
                .tooltip("Context window nearing limit")
            }

            // Task progress + completion ETA share one line: dots left, ETA
            // right (issue #558).
            taskProgressRow

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
        // Fade, don't delete (#540): a row whose relay daemon has gone offline
        // dims in place and restores on reconnect, so a flapping link doesn't
        // yank rows. Local sessions are never offline.
        .opacity(sessionManager.isOffline(session) ? 0.4 : 1)
        .animation(IrrMotion.easeOut(duration: IrrMotion.fast), value: sessionManager.isOffline(session))
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

    /// The session's task list when it should render — non-empty and not
    /// fully completed (same gate the standalone dots row used).
    private var activeTasks: [SessionTask]? {
        guard let tasks = session.metrics?.tasks, !tasks.isEmpty, !tasks.allSatisfy(\.isCompleted) else { return nil }
        return tasks
    }

    private struct TaskEtaPresentation {
        let text: String
        let stale: Bool
        let title: String
    }

    /// One sub-row: task dots left, completion ETA right (issue #558). It's a
    /// sub-row rather than part of the main HStack because the ETA, placed
    /// inline next to the cost, truncated to "…" at menu-bar width (the model
    /// label's layoutPriority wins the squeeze); the ETA label is fixed-size
    /// so the wrapping dots can't compress it. taskEtaPresentation() is
    /// computed exactly once here.
    @ViewBuilder
    private var taskProgressRow: some View {
        let eta = taskEtaPresentation()
        if activeTasks != nil || eta != nil {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                if let tasks = activeTasks {
                    TaskListView(tasks: tasks)
                }
                Spacer(minLength: 8)
                if let eta, let est = session.metrics?.taskEstimate {
                    // Progress as a percentage — the raw rounds (5/10) read
                    // like a second task counter next to the dots; the tooltip
                    // still carries the exact rounds.
                    let percent = Int((Double(est.completedRounds) / Double(max(est.totalRounds, 1)) * 100).rounded())
                    HStack(spacing: 4) {
                        Image(systemName: "timer")
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                        Text("\(eta.text) · \(percent)%")
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                    }
                    .fixedSize()
                    .opacity(eta.stale ? 0.5 : 1.0)
                    .tooltip(eta.title)
                }
            }
            .padding(.top, 2)
        }
    }

    /// Decides the task-completion ETA chip (issue #558) — mirrors the web's
    /// taskEtaPresentation. Nil hides the chip: session not working or no
    /// estimate. Zero completed rounds renders a progress-only "estimating…"
    /// chip (#602); with progress, a range whose high bound stays pinned at
    /// the last marker — 1.5× padded below half the rounds, bare at/above
    /// half so it collapses to a point right at a marker and widens instead
    /// of counting down (#616) — and stale dimming when the last marker is
    /// older than 3 minutes.
    private func taskEtaPresentation(now: Date = Date()) -> TaskEtaPresentation? {
        guard session.state == .working,
              let metrics = session.metrics,
              let est = metrics.taskEstimate else { return nil }
        let sourceLabel = est.source == "tasks" ? "from task list"
            : est.source == "subagents" ? "from subagents" : "agent-reported"
        guard est.completedRounds > 0 else {
            // No measurable rate yet, but the agent committed to a plan —
            // immediate feedback beats waiting for the first round (#602).
            guard est.totalRounds > 0 else { return nil }
            var stale = false
            var title = "Task ETA — \(sourceLabel) 0/\(est.totalRounds) rounds"
            if let updated = est.updatedAt {
                let age = max(0, now.timeIntervalSince(updated))
                stale = age > 180
                title += ", updated \(Int(age))s ago"
            }
            return TaskEtaPresentation(text: "estimating…", stale: stale, title: title)
        }
        guard let eta = metrics.taskCompletionEta else {
            // Progress without a projection (e.g. a subagent aggregate whose
            // children carry no etas yet, #626): show a rounds-only chip
            // rather than hiding one that was visible moments ago.
            var stale = false
            var title = "Task ETA — \(sourceLabel) \(est.completedRounds)/\(est.totalRounds) rounds"
            if let updated = est.updatedAt {
                let age = max(0, now.timeIntervalSince(updated))
                stale = age > 180
                title += ", updated \(Int(age))s ago"
            }
            return TaskEtaPresentation(
                text: "\(est.completedRounds)/\(est.totalRounds)", stale: stale, title: title)
        }
        let remaining = max(0, eta.timeIntervalSince(now))
        let frac = est.totalRounds > 0 ? Double(est.completedRounds) / Double(est.totalRounds) : 0
        // The eta is anchored at the marker (daemon-side): the LOW bound
        // counts down between marker updates while the HIGH bound stays
        // pinned at the marker until the agent reports fresh progress —
        // 1.5× the projected remaining time while the rate is barely
        // measurable (below half the rounds), the bare projected remaining
        // once it's trusted, so the point estimate widens instead of
        // counting down naked (#616). No marker timestamp at/above half →
        // nothing to pin to, keep the point estimate.
        let factor = frac < 0.5 ? 1.5 : 1.0
        var highSecs: TimeInterval? = nil
        if let updated = est.updatedAt {
            highSecs = max(remaining, eta.timeIntervalSince(updated) * factor)
        } else if frac < 0.5 {
            highSecs = remaining * 1.5
        }
        let text = etaText(remaining: remaining, highSecs: highSecs)
        var stale = false
        var title = "Task ETA — \(sourceLabel) \(est.completedRounds)/\(est.totalRounds) rounds"
        if let updated = est.updatedAt {
            let age = max(0, now.timeIntervalSince(updated))
            stale = age > 180
            title += ", updated \(Int(age))s ago"
        }
        return TaskEtaPresentation(text: text, stale: stale, title: title)
    }

    /// Renders the remaining-time text with exactly ONE sign — "~"
    /// (approximate) or "<" (upper bound), never both, never a degenerate
    /// "2m–2m" range (mirrors the web's fmtEtaText). highSecs nil → point.
    ///   point, ≥1m → "~12m left" · point, <1m → "<1m left"
    ///   range, low <1m → "<2m left" (collapses to its upper bound)
    ///   range, low==high → point rules · range → "~8m–12m left"
    private func etaText(remaining: TimeInterval, highSecs: TimeInterval?) -> String {
        let low = etaDurationString(remaining)
        if let highSecs {
            let high = etaDurationString(highSecs)
            if low != high {
                if remaining < 60 { return "<\(high) left" }
                return "~\(low)–\(high) left"
            }
        }
        if remaining < 60 { return "<1m left" }
        return "~\(low) left"
    }

    /// Minute-resolution duration for the ETA chip — second-level detail
    /// would flicker for a number that is inherently rough.
    private func etaDurationString(_ seconds: TimeInterval) -> String {
        if seconds < 60 { return "<1m" }
        let mins = Int((seconds / 60).rounded())
        let h = mins / 60
        let m = mins % 60
        if h > 0 { return m > 0 ? "\(h)h\(m)m" : "\(h)h" }
        return "\(m)m"
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

/// Backchannel control affordance (#724): a keyboard button that opens a
/// popover to send text or an interrupt into a controllable session. Shown only
/// when `session.controllable`. The whole-row tap (focus) is unaffected —
/// SwiftUI gives the button its own tap handling.
struct SessionControlButton: View {
    let session: SessionState
    @EnvironmentObject var sessionManager: SessionManager
    @State private var showPopover = false
    @State private var draft = ""

    var body: some View {
        Button {
            showPopover.toggle()
        } label: {
            Image(systemName: "keyboard")
                .font(.system(size: 10))
                .foregroundColor(.secondary)
        }
        .buttonStyle(.plain)
        .tooltip("Send input to this session")
        .accessibilityIdentifier("session-control-\(session.id)")
        .popover(isPresented: $showPopover, arrowEdge: .bottom) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Send to session").font(.caption).foregroundColor(.secondary)
                HStack(spacing: 6) {
                    TextField("text or /command", text: $draft)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 220)
                        .onSubmit(send)
                    Button("Send", action: send).disabled(draft.isEmpty)
                }
                Button(role: .destructive) {
                    Task { _ = await sessionManager.interruptSession(sessionId: session.id) }
                    showPopover = false
                } label: {
                    Label("Interrupt (Ctrl-C)", systemImage: "stop.circle")
                }
                .buttonStyle(.borderless)
            }
            .padding(12)
        }
    }

    private func send() {
        let text = draft
        guard !text.isEmpty else { return }
        // Append a return so the line submits (mirrors the tmux send-keys path).
        Task { _ = await sessionManager.sendInput(sessionId: session.id, text: text + "\r") }
        draft = ""
        showPopover = false
    }
}

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
            SessionStateIcon(state: session.state, size: 12)
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

            // Duration — driven by the shared 1 Hz DurationClock so N subagent
            // rows cost one timer, not N independent TimelineView schedulers (#690).
            if let metrics = session.metrics {
                let isActive = session.state == .working || session.state == .waiting
                LiveDurationText(isActive: isActive, metrics: metrics, firstSeen: session.firstSeen)
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

// MARK: - Shared duration clock (#690)

/// One 1 Hz clock shared by every live duration label. Replaces the per-row
/// `TimelineView(.periodic)` schedulers so N visible rows cost a single timer
/// instead of N. Only the leaf `LiveDurationText` views observe it, so a tick
/// re-renders just those labels — never their parent rows.
@MainActor
final class DurationClock: ObservableObject {
    static let shared = DurationClock()
    /// Bumped every second purely to invalidate observing labels; the duration
    /// strings read the wall clock themselves, so the value is never consumed.
    @Published private(set) var tick: UInt64 = 0
    private var timer: Timer?

    private init() {
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.tick &+= 1 }
        }
    }
}

/// A duration label that refreshes once per second off the shared `DurationClock`.
/// Active sessions show realtime elapsed; finished ones show their frozen total.
private struct LiveDurationText: View {
    @ObservedObject private var clock = DurationClock.shared
    let isActive: Bool
    let metrics: SessionMetrics
    let firstSeen: Date

    var body: some View {
        Text(isActive
            ? metrics.formattedRealtimeElapsedTime(sessionFirstSeen: firstSeen)
            : (metrics.elapsedSeconds > 0 ? metrics.formattedElapsedTime : "—"))
            .font(.caption2)
            .foregroundColor(.secondary.opacity(0.7))
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

                // Rig/codebase status (Codebase.Status) on nested rig headers.
                if !isTopLevel, let status = group.status, !status.isEmpty {
                    Text(status.uppercased())
                        .font(.system(size: 9, weight: .semibold, design: .monospaced))
                        .foregroundColor(IrrColors.forState(status))
                        .tooltip("Rig status: \(status)")
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
                ForEach(Array((group.agents ?? []).enumerated()), id: \.element.rowID) { index, session in
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