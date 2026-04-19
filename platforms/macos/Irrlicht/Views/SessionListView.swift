import AppKit
import SwiftUI

// MARK: - Tooltip support for MenuBarExtra

/// Forces a native NSView tooltip on any SwiftUI view.
/// `.help()` doesn't work inside MenuBarExtra panels, so we bridge to AppKit.
private struct TooltipView: NSViewRepresentable {
    let text: String
    func makeNSView(context: Context) -> NSView {
        let view = NSView()
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

struct SessionListView: View {
    @EnvironmentObject var sessionManager: SessionManager
    @EnvironmentObject var gasTownProvider: GasTownProvider
    @State private var isQuitButtonHovered = false
    @State private var isSettingsButtonHovered = false
    @State private var showSettings = false

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
            .frame(width: 350)
            .background(Color(NSColor.windowBackgroundColor))
        }
    }

    // MARK: - Group List (renders apiGroups directly)

    private var groupListContent: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                ForEach(sessionManager.apiGroups) { group in
                    GroupView(group: group)
                }
            }
        }
        .frame(maxHeight: 400)
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
            
            statusIndicator
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
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

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.secondary.opacity(0.15))
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color(hex: pressureColor))
                    .frame(width: geo.size.width * min(CGFloat(utilization) / 100, 1.0))
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
    @State private var isHovered = false

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

                // Branch name (project name is in the group header)
                Text(session.gitBranch ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.primary)
                    .lineLimit(1)
                    .tooltip(session.gitBranch ?? "—")

                // Context utilization bar
                if let metrics = session.metrics, metrics.hasContextData {
                    ContextBar(utilization: metrics.contextUtilization,
                               pressureColor: metrics.contextPressureColor)
                        .frame(maxWidth: 80, maxHeight: 8)
                    Text(metrics.formattedContextUtilization)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundColor(Color(hex: metrics.contextPressureColor))
                } else if debugMode, let metrics = session.metrics, metrics.totalTokens > 0 {
                    Text(metrics.formattedTokenUsage)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundColor(Color(hex: "#8E8E93"))
                }

                // Estimated cost
                if showCostDisplay, let cost = session.metrics?.formattedCost {
                    Text(cost)
                        .font(.system(size: 9, weight: .medium, design: .monospaced))
                        .foregroundColor(.secondary)
                }

                Spacer()

                // Short model name + adapter icon
                Text(session.shortModelName)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .tooltip(session.effectiveModel)
                    .accessibilityIdentifier("session-model-label-\(session.id)")
                if let icon = session.adapterIcon {
                    Image(nsImage: icon)
                        .frame(width: 12, height: 12)
                        .tooltip(session.adapterName)
                }

                // Action buttons on hover
                if isHovered {
                    SessionActionButtons(session: session)
                }
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
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("session-card-\(session.id)")
        .accessibilityLabel("\(session.projectName ?? "unknown") \(session.state.rawValue) \(session.shortModelName)")
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

// MARK: - Session Action Buttons

struct SessionActionButtons: View {
    let session: SessionState
    @EnvironmentObject var sessionManager: SessionManager
    
    var body: some View {
        HStack(spacing: 4) {
            // Reset button
            Button(action: {
                sessionManager.resetSessionState(sessionId: session.id)
            }) {
                Image(systemName: "arrow.counterclockwise")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("Reset to ready state")
            
            // Delete button
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
    @State private var isExpanded = true
    @AppStorage("projectCostTimeframe") private var costTimeframeRaw: String = CostTimeframe.day.rawValue
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false

    private var costTimeframe: CostTimeframe { CostTimeframe.from(costTimeframeRaw) }

    private var agentCount: Int {
        let direct = group.agents?.count ?? 0
        let nested = (group.groups ?? []).reduce(0) { $0 + ($1.agents?.count ?? 0) }
        return direct + nested
    }

    private var isTopLevel: Bool { depth == 0 }

    /// Formatted cost for this group in the currently-selected time frame,
    /// or nil if no data.
    private var formattedCost: String? {
        guard showCostDisplay, isTopLevel, !group.isGasTown else { return nil }
        guard let v = group.costs?[costTimeframe.rawValue], v > 0 else { return nil }
        let formatted: String
        if v < 0.01 { formatted = "<$0.01" }
        else if v < 10 { formatted = String(format: "$%.2f", v) }
        else { formatted = String(format: "$%.0f", v) }
        return formatted + costTimeframe.suffix
    }

    private func cycleCostTimeframe() {
        costTimeframeRaw = costTimeframe.next().rawValue
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Button(action: {
                    withAnimation(.easeInOut(duration: 0.15)) {
                        isExpanded.toggle()
                    }
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
                            lastAssistantText: nil
                        )
                    )
                ]
                return manager
            }())
    }
}