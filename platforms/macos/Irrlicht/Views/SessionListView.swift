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
    
    // MARK: - Session List

    private var sessionGroups: [SessionGroup] {
        // sessions = top-level only (no children in cycle)
        // allSessions = includes children for badge counting
        let topLevel: [SessionState]
        if gasTownProvider.isAvailable {
            topLevel = sessionManager.sessions.filter { !gasTownProvider.ownsSession($0) }
        } else {
            topLevel = sessionManager.sessions
        }
        let all = sessionManager.allSessions

        return topLevel.map { parent in
            let subagents = all.filter { $0.parentSessionId == parent.id }
            return SessionGroup(parent: parent, subagents: subagents)
        }
    }

    private var projectGroups: [ProjectGroup] {
        let groups = sessionGroups

        // Group session groups by project name (from git repo root) or full cwd path.
        // Sessions in different worktrees of the same repo share the same project name.
        var grouped: [String: [SessionGroup]] = [:]
        for group in groups {
            let key = group.parent.projectName ?? group.parent.cwd
            grouped[key, default: []].append(group)
        }

        // Convert to ProjectGroup array, ordered by user preference
        let unsorted = grouped.map { key, value in
            let display = value.first?.parent.projectName
                ?? URL(fileURLWithPath: key).lastPathComponent
            return ProjectGroup(projectDirectory: key, displayName: display, sessionGroups: value)
        }
        return sessionManager.orderedProjectGroups(from: unsorted)
    }

    private var sessionListContent: some View {
        let allProjectGroups = projectGroups
        let isCompactMode = allProjectGroups.count > 5
        // Pre-compute starting flat index for each project group (for drag/drop continuity)
        var startIndices: [String: Int] = [:]
        var runningIndex = 0
        for pg in allProjectGroups {
            startIndices[pg.id] = runningIndex
            runningIndex += pg.sessionGroups.count
        }
        let totalGroups = runningIndex

        let projectGroupCount = allProjectGroups.count

        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 1) {
                ForEach(Array(allProjectGroups.enumerated()), id: \.element.id) { pgIndex, projectGroup in
                    ProjectGroupSectionView(
                        projectGroup: projectGroup,
                        startingGroupIndex: startIndices[projectGroup.id] ?? 0,
                        isCompact: isCompactMode,
                        projectGroupIndex: pgIndex,
                        totalProjectGroups: projectGroupCount
                    )
                    .onDrop(of: [.text], delegate: ProjectGroupDropDelegate(
                        sessionManager: sessionManager,
                        targetProjectGroupIndex: pgIndex,
                        allProjectDirectories: Set(allProjectGroups.map(\.projectDirectory))
                    ))
                }

                // Drop zone at the end of the list (for sessions)
                Rectangle()
                    .fill(Color.clear)
                    .frame(height: 20)
                    .onDrop(of: [.text], delegate: SessionGroupDropDelegate(
                        sessionManager: sessionManager,
                        targetGroupIndex: totalGroups
                    ))
            }
        }
        .frame(maxHeight: 400)
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
    @State private var isExpanded = true

    private var agentCount: Int {
        let direct = group.agents?.count ?? 0
        let nested = (group.groups ?? []).reduce(0) { $0 + ($1.agents?.count ?? 0) }
        return direct + nested
    }

    private var isTopLevel: Bool { depth == 0 }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
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

                    Spacer()

                    let count = isTopLevel ? agentCount : (group.agents?.count ?? 0)
                    Text(isTopLevel ? "\(count) \(count == 1 ? "session" : "sessions")" : "\(count)")
                        .font(.caption2)
                        .foregroundColor(.secondary.opacity(isTopLevel ? 0.7 : 0.5))
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
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
}

// MARK: - Session Group

struct SessionGroup: Identifiable {
    let parent: SessionState
    let subagents: [SessionState]

    var id: String { parent.id }
    var allSessions: [SessionState] { [parent] + subagents }
}

struct ProjectGroup: Identifiable {
    let projectDirectory: String   // full path or project name (grouping key)
    let displayName: String        // short name shown in UI
    let sessionGroups: [SessionGroup]

    var id: String { projectDirectory }

    /// States in display order (maps left-to-right dots to top-to-bottom sessions).
    var sessionStates: [SessionState.State] {
        sessionGroups.map { $0.parent.state }
    }

    /// Highest-priority state across all sessions (waiting > working > ready).
    var dominantState: SessionState.State {
        .dominant(in: sessionStates)
    }
}

struct SessionStateDots: View {
    let projectGroup: ProjectGroup
    let isCompact: Bool

    private var stateCounts: (waiting: Int, working: Int, ready: Int) {
        var w = 0, k = 0, r = 0
        for s in projectGroup.sessionStates {
            switch s {
            case .waiting: w += 1
            case .working: k += 1
            case .ready:   r += 1
            }
        }
        return (w, k, r)
    }

    var body: some View {
        if isCompact {
            compactDot
        } else if projectGroup.sessionStates.count > 4 {
            overflowCounts
        } else {
            normalDots
        }
    }

    // MARK: Normal mode (≤4 sessions): individual dots

    private var normalDots: some View {
        HStack(spacing: 3) {
            ForEach(Array(projectGroup.sessionStates.enumerated()), id: \.offset) { _, state in
                dotForState(state)
            }
        }
        .tooltip(tooltipText)
    }

    private func dotForState(_ state: SessionState.State) -> some View {
        Circle()
            .fill(Color(hex: state.color))
            .frame(width: 6, height: 6)
    }

    // MARK: Overflow mode (>4 sessions): state counts

    private var overflowCounts: some View {
        let c = stateCounts
        return HStack(spacing: 4) {
            if c.waiting > 0 {
                stateCountLabel("●", count: c.waiting, color: Color(hex: SessionState.State.waiting.color))
            }
            if c.working > 0 {
                stateCountLabel("●", count: c.working, color: Color(hex: SessionState.State.working.color))
            }
            if c.ready > 0 {
                stateCountLabel("●", count: c.ready, color: Color(hex: SessionState.State.ready.color))
            }
        }
        .tooltip(tooltipText)
    }

    private func stateCountLabel(_ symbol: String, count: Int, color: Color) -> some View {
        HStack(spacing: 1) {
            Text(symbol)
                .font(.system(size: 8))
                .foregroundColor(color)
            Text("\(count)")
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(color)
        }
    }

    // MARK: Compact mode (many groups): single dominant dot

    private var compactDot: some View {
        Circle()
            .fill(Color(hex: projectGroup.dominantState.color))
            .frame(width: 6, height: 6)
            .tooltip(tooltipText)
    }

    private var tooltipText: String {
        let c = stateCounts
        var parts: [String] = []
        if c.waiting > 0 { parts.append("\(c.waiting) waiting") }
        if c.working > 0 { parts.append("\(c.working) working") }
        if c.ready > 0 { parts.append("\(c.ready) ready") }
        return parts.joined(separator: ", ")
    }
}

// MARK: - Project Group Section View

struct ProjectGroupSectionView: View {
    let projectGroup: ProjectGroup
    let startingGroupIndex: Int
    let isCompact: Bool
    let projectGroupIndex: Int
    let totalProjectGroups: Int
    @EnvironmentObject var sessionManager: SessionManager
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @State private var isExpanded = true

    /// Combined cost of all sessions in the group
    private var totalCost: Double {
        projectGroup.sessionGroups.reduce(0) { sum, group in
            sum + (group.parent.metrics?.estimatedCostUSD ?? 0)
                + group.subagents.reduce(0) { $0 + ($1.metrics?.estimatedCostUSD ?? 0) }
        }
    }

    /// Maximum context utilization across all sessions in the group
    private var maxContextUtilization: Double {
        projectGroup.sessionGroups.flatMap { [$0.parent] + $0.subagents }
            .compactMap { $0.metrics?.contextUtilization }
            .max() ?? 0
    }

    /// Color for the project name based on max context utilization
    private var projectNameColor: Color {
        if maxContextUtilization > 90 { return Color(hex: "#FF3B30") }   // red
        if maxContextUtilization > 75 { return Color(hex: "#FF9500") }   // orange
        if maxContextUtilization > 50 { return Color(hex: "#FFCC00") }   // yellow
        return Color(hex: "#34C759")                                      // green
    }

    private var formattedTotalCost: String? {
        guard totalCost > 0 else { return nil }
        if totalCost < 0.01 { return "<$0.01" }
        if totalCost < 10 { return String(format: "$%.2f", totalCost) }
        return String(format: "$%.0f", totalCost)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Collapsible project header with reorder arrows
            HStack(spacing: 0) {
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

                        Text(projectGroup.displayName)
                            .font(.system(.caption, design: .monospaced))
                            .fontWeight(.semibold)
                            .foregroundColor(projectNameColor)

                        if showCostDisplay, let cost = formattedTotalCost {
                            Text(cost)
                                .font(.system(size: 9, weight: .medium, design: .monospaced))
                                .foregroundColor(.secondary)
                        }

                        SessionStateDots(projectGroup: projectGroup, isCompact: isCompact)

                        Spacer()

                        let count = projectGroup.sessionGroups.count
                        Text("\(count) \(count == 1 ? "session" : "sessions")")
                            .font(.caption2)
                            .foregroundColor(.secondary.opacity(0.7))
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)

                if totalProjectGroups > 1 {
                    HStack(spacing: 0) {
                        Button(action: {
                            sessionManager.moveProjectGroupUp(projectDirectory: projectGroup.projectDirectory)
                        }) {
                            Image(systemName: "chevron.up")
                                .font(.system(size: 10))
                                .foregroundColor(.secondary)
                                .frame(width: 14, height: 20)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(projectGroupIndex == 0)
                        .opacity(projectGroupIndex == 0 ? 0.3 : 1.0)

                        Button(action: {
                            sessionManager.moveProjectGroupDown(projectDirectory: projectGroup.projectDirectory)
                        }) {
                            Image(systemName: "chevron.down")
                                .font(.system(size: 10))
                                .foregroundColor(.secondary)
                                .frame(width: 14, height: 20)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(projectGroupIndex == totalProjectGroups - 1)
                        .opacity(projectGroupIndex == totalProjectGroups - 1 ? 0.3 : 1.0)
                    }
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 4)
            .onDrag {
                NSItemProvider(object: projectGroup.projectDirectory as NSString)
            }

            // Session rows (indented under the project header)
            if isExpanded {
                ForEach(Array(projectGroup.sessionGroups.enumerated()), id: \.element.id) { localIndex, group in
                    // The daemon publishes a unified subagent summary (in-process
                    // from the adapter plus file-based children) on both REST and
                    // WebSocket paths; see ComputeSubagentSummary in core.
                    let activeCount = (group.parent.subagents?.working ?? 0)
                        + (group.parent.subagents?.waiting ?? 0)
                    SessionRowView(session: group.parent, agentNumber: localIndex + 1, activeSubagentCount: activeCount)
                        .padding(.leading, 8)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            print("Selected session: \(group.parent.id)")
                        }
                        .onDrag {
                            NSItemProvider(object: group.parent.id as NSString)
                        }
                        .onDrop(of: [.text], delegate: SessionGroupDropDelegate(
                            sessionManager: sessionManager,
                            targetGroupIndex: startingGroupIndex + localIndex
                        ))

                    // Subagent rows (indented, compact) — debug mode only
                    if debugMode {
                        ForEach(group.subagents) { subagent in
                            SubagentRowView(session: subagent)
                                .padding(.leading, 8)
                        }
                    }
                }
            }
        }
        .accessibilityIdentifier("project-group-\(projectGroup.projectDirectory)")
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

// MARK: - Drag and Drop Support

struct SessionGroupDropDelegate: DropDelegate {
    let sessionManager: SessionManager
    let targetGroupIndex: Int

    func validateDrop(info: DropInfo) -> Bool {
        return info.hasItemsConforming(to: [.text])
    }

    func performDrop(info: DropInfo) -> Bool {
        guard let itemProvider = info.itemProviders(for: [.text]).first else {
            return false
        }

        itemProvider.loadObject(ofClass: NSString.self) { item, error in
            guard let parentId = item as? String else { return }
            DispatchQueue.main.async {
                sessionManager.reorderGroup(parentId: parentId, to: targetGroupIndex)
            }
        }

        return true
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        return DropProposal(operation: .move)
    }
}

// MARK: - Project Group Drag and Drop

struct ProjectGroupDropDelegate: DropDelegate {
    let sessionManager: SessionManager
    let targetProjectGroupIndex: Int
    let allProjectDirectories: Set<String>

    func validateDrop(info: DropInfo) -> Bool {
        return info.hasItemsConforming(to: [.text])
    }

    func performDrop(info: DropInfo) -> Bool {
        guard let itemProvider = info.itemProviders(for: [.text]).first else {
            return false
        }

        itemProvider.loadObject(ofClass: NSString.self) { item, error in
            guard let projectDirectory = item as? String,
                  allProjectDirectories.contains(projectDirectory) else { return }
            DispatchQueue.main.async {
                sessionManager.reorderProjectGroup(
                    projectDirectory: projectDirectory,
                    to: targetProjectGroupIndex
                )
            }
        }

        return true
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        return DropProposal(operation: .move)
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