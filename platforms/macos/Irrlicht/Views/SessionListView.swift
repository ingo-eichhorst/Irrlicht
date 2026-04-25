import AppKit
import SwiftUI

// MARK: - Tooltip support for MenuBarExtra

/// Forces a native NSView tooltip on any SwiftUI view.
/// `.help()` doesn't work inside MenuBarExtra panels, so we bridge to AppKit.
/// `hitTest` returns nil so the overlay doesn't swallow clicks meant for
/// interactive views (buttons) underneath.
private final class PassThroughTooltipView: NSView {
    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

private struct TooltipView: NSViewRepresentable {
    let text: String
    func makeNSView(context: Context) -> NSView {
        let view = PassThroughTooltipView()
        view.toolTip = text
        return view
    }
    func updateNSView(_ nsView: NSView, context: Context) {
        nsView.toolTip = text
    }
}

extension View {
    func tooltip(_ text: String) -> some View {
        overlay(TooltipView(text: text))
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
                            .background(isSettingsButtonHovered ? Color.accentColor.opacity(0.1) : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isSettingsButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)

                    Divider().frame(height: 20)

                    Button(action: { NSApplication.shared.terminate(nil) }) {
                        Text("Quit")
                            .foregroundColor(.secondary)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(isQuitButtonHovered ? Color.accentColor.opacity(0.1) : Color.clear)
                            .contentShape(Rectangle())
                            .onHover { hovering in
                                isQuitButtonHovered = hovering
                            }
                    }
                    .buttonStyle(.plain)
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
            Image(systemName: "lightbulb.slash")
                .font(.system(size: 24))
                .foregroundColor(.secondary)

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
            HStack(spacing: 4) {
                Text("Irrlicht v\(appVersion)")
                    .font(.headline)
                    .foregroundColor(.primary)

                Text("—")
                    .foregroundColor(.secondary)

                sessionIconsView
            }

            Spacer()

            Button {
                let next = displayMode.next()
                displayModeRaw = next.rawValue
                if next.isHistory {
                    sessionManager.startHistoryPolling(granularitySec: next.granularitySec)
                } else {
                    sessionManager.stopHistoryPolling()
                }
            } label: {
                Text(displayMode.rawValue)
                    .font(.system(size: 10, design: .monospaced))
                    .frame(width: 44)
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(displayMode.isHistory ? Color.accentColor.opacity(0.15) : Color.clear)
            .cornerRadius(4)
            .overlay(RoundedRectangle(cornerRadius: 4).stroke(Color.secondary.opacity(0.4)))
            .contentShape(Rectangle())
            .tooltip(displayMode.tooltip)
            .id("mode-cycle-btn")

            statusIndicator
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .onAppear {
            if displayMode.isHistory {
                sessionManager.startHistoryPolling(granularitySec: displayMode.granularitySec)
            }
        }
        .onDisappear {
            sessionManager.stopHistoryPolling()
        }
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
                        .foregroundColor(Color(hex: session.state.color))
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
            Text(statusLabel)
                .font(.caption2)
                .foregroundColor(.secondary)
        }
    }

    private var statusColor: Color {
        switch sessionManager.connectionState {
        case .connected: return .green
        case .connecting, .reconnecting: return .yellow
        case .disconnected: return .red
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
        HStack {
            Image(systemName: "exclamationmark.triangle")
                .foregroundColor(.orange)
            
            Text(error)
                .font(.caption)
                .foregroundColor(.secondary)
                .lineLimit(2)
            
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color.orange.opacity(0.1))
    }
}

// MARK: - Session Row View

struct ContextBar: View {
    let utilization: Double
    let pressureColor: String
    var label: String? = nil

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.secondary.opacity(0.15))
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color(hex: pressureColor))
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
                    .foregroundColor(Color(hex: session.state.color))
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
                        .background(Color.purple)
                        .clipShape(Circle())
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
                        if showCostDisplay {
                            Text(metrics.formattedCost ?? "")
                                .font(.system(size: 9, weight: .medium, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 36, alignment: .leading)
                        } else {
                            Text(metrics.formattedContextUtilization)
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundColor(Color(hex: metrics.contextPressureColor))
                                .frame(width: 32, alignment: .leading)
                        }
                    } else if debugMode, let metrics = session.metrics, metrics.totalTokens > 0 {
                        Color.clear.frame(width: 100, height: 13)
                        Text(metrics.formattedTokenUsage)
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(Color(hex: "#8E8E93"))
                            .frame(width: 32, alignment: .leading)
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
                    .foregroundColor(.orange)
                    .lineLimit(3)
                    .truncationMode(.head)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 3)
                    .background(Color.orange.opacity(0.12))
                    .cornerRadius(4)
                    .padding(.top, 2)
            }

            // Context pressure alert (80%+, active sessions only)
            if let metrics = session.metrics,
               (session.state == .working || session.state == .waiting),
               metrics.contextUtilization >= 80 {
                let isCritical = metrics.contextUtilization >= 95
                HStack(spacing: 4) {
                    Image(systemName: isCritical ? "exclamationmark.triangle.fill" : "exclamationmark.triangle")
                        .font(.system(size: 9))
                        .foregroundColor(isCritical ? .red : .orange)
                    Text("Switch to a fresh session soon")
                        .font(.system(size: 9))
                        .foregroundColor(isCritical ? .red : .orange)
                    Spacer()
                }
                .padding(.horizontal, 4)
                .padding(.vertical, 2)
                .background((isCritical ? Color.red : Color.orange).opacity(0.08))
                .cornerRadius(4)
                .padding(.top, 2)
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
                            .help("Click to copy full ID")
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
        .background(isHovered ? Color.accentColor.opacity(0.1) : Color.clear)
        .contentShape(Rectangle())
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
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
                        Circle().fill(Color.green.opacity(0.85))
                    } else {
                        Circle().strokeBorder(Color(hex: "#8B5CF6"), lineWidth: 1.5)
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
            .help("Reset to ready state")

            Button(action: {
                sessionManager.deleteSession(sessionId: session.id)
            }) {
                Image(systemName: "trash")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("Delete session")
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
                .foregroundColor(Color(hex: session.state.color))
                .frame(width: 12)
                .tooltip(session.state.label)

            // Context utilization
            if let metrics = session.metrics, metrics.hasContextData {
                Text(metrics.formattedContextUtilization)
                    .font(.caption2)
                    .foregroundColor(Color(hex: metrics.contextPressureColor))
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
        .background(isHovered ? Color.accentColor.opacity(0.05) : Color.clear)
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
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

                if let cost = formattedCost {
                    Button(action: cycleCostTimeframe) {
                        Text(cost)
                            .font(.system(size: 9, weight: .medium, design: .monospaced))
                            .foregroundColor(.secondary)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Click to cycle time frame (day → week → month → year)")
                }

                Spacer()

                let count = isTopLevel ? agentCount : (group.agents?.count ?? 0)
                Text(isTopLevel ? "\(count) \(count == 1 ? "session" : "sessions")" : "\(count)")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(isTopLevel ? 0.7 : 0.5))

                if isTopLevel, sessionManager.apiGroups.count > 1,
                   let idx = sessionManager.apiGroups.firstIndex(where: { $0.name == group.name }) {
                    HStack(spacing: 0) {
                        reorderButton(icon: "chevron.up", disabled: idx == 0) {
                            sessionManager.moveProjectGroupUp(name: group.name)
                        }
                        reorderButton(icon: "chevron.down", disabled: idx == sessionManager.apiGroups.count - 1) {
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

    private func reorderButton(icon: String, disabled: Bool, action: @escaping () -> Void) -> some View {
        Button {
            withAnimation(.easeInOut(duration: 0.22)) { action() }
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

// MARK: - Color Extension

extension Color {
    init(hex: String) {
        let hex = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        var int: UInt64 = 0
        Scanner(string: hex).scanHexInt64(&int)
        let a, r, g, b: UInt64
        switch hex.count {
        case 3: // RGB (12-bit)
            (a, r, g, b) = (255, (int >> 8) * 17, (int >> 4 & 0xF) * 17, (int & 0xF) * 17)
        case 6: // RGB (24-bit)
            (a, r, g, b) = (255, int >> 16, int >> 8 & 0xFF, int & 0xFF)
        case 8: // ARGB (32-bit)
            (a, r, g, b) = (int >> 24, int >> 16 & 0xFF, int >> 8 & 0xFF, int & 0xFF)
        default:
            (a, r, g, b) = (1, 1, 1, 0)
        }

        self.init(
            .sRGB,
            red: Double(r) / 255,
            green: Double(g) / 255,
            blue:  Double(b) / 255,
            opacity: Double(a) / 255
        )
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