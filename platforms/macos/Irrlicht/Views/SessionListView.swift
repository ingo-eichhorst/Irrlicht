import SwiftUI

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
                if gasTownProvider.isAvailable {
                    gasTownHeaderView
                    Divider()
                    gasTownContentView
                    if !sessionManager.sessions.isEmpty {
                        Divider()
                        Text("Other Sessions")
                            .font(.system(.caption, design: .monospaced))
                            .fontWeight(.semibold)
                            .foregroundColor(.secondary)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 4)
                        sessionListContent
                    }
                } else if sessionManager.sessions.isEmpty {
                    emptyStateView
                } else {
                    sessionHeaderView
                    Divider()
                    sessionListContent
                }

                if let error = sessionManager.lastError {
                    Divider()
                    errorView(error)
                }

                // Settings and Quit buttons at bottom
                Divider()
                Button("Settings…") {
                    showSettings = true
                }
                .buttonStyle(.plain)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 6)
                .onHover { hovering in
                    isSettingsButtonHovered = hovering
                    if hovering {
                        NSCursor.pointingHand.push()
                    } else {
                        NSCursor.pop()
                    }
                }
                .background(
                    RoundedRectangle(cornerRadius: 4)
                        .fill(isSettingsButtonHovered ? Color.accentColor.opacity(0.1) : Color.clear)
                        .animation(.easeInOut(duration: 0.2), value: isSettingsButtonHovered)
                )

                Divider()
                Button("Quit Irrlicht") {
                    NSApplication.shared.terminate(nil)
                }
                .buttonStyle(.plain)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 8)
                .onHover { hovering in
                    isQuitButtonHovered = hovering
                    if hovering {
                        NSCursor.pointingHand.push()
                    } else {
                        NSCursor.pop()
                    }
                }
                .background(
                    RoundedRectangle(cornerRadius: 4)
                        .fill(isQuitButtonHovered ? Color.accentColor.opacity(0.1) : Color.clear)
                        .animation(.easeInOut(duration: 0.2), value: isQuitButtonHovered)
                )
            }
            .frame(width: 350)
            .background(Color(NSColor.windowBackgroundColor))
        }
    }
    
    // MARK: - Gas Town Header

    private var gasTownHeaderView: some View {
        HStack {
            HStack(spacing: 6) {
                Text("⛽")
                    .font(.system(size: 12))
                Text("Gas Town")
                    .font(.headline)
                    .foregroundColor(.primary)

                if gasTownProvider.activeRigCount > 0 {
                    Text("x\(gasTownProvider.activeRigCount)")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundColor(.secondary)
                }
            }

            Spacer()

            if gasTownProvider.isDaemonRunning {
                HStack(spacing: 4) {
                    Circle()
                        .fill(Color.green)
                        .frame(width: 6, height: 6)
                    Text("running")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    // MARK: - Gas Town Content (global agents + convoys + rigs)

    private var gasTownContentView: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                // Global agents (Mayor, Deacon)
                ForEach(gasTownProvider.globalAgents) { agent in
                    GlobalAgentRowView(agent: agent)
                }

                // Convoys section
                if !gasTownProvider.convoys.isEmpty {
                    ConvoySectionView(convoys: gasTownProvider.convoys)
                }

                // Codebases (rig rows with worktrees)
                ForEach(gasTownProvider.codebases) { codebase in
                    CodebaseRowView(codebase: codebase)
                }

                if gasTownProvider.codebases.isEmpty && gasTownProvider.globalAgents.isEmpty {
                    HStack {
                        Spacer()
                        Text("No rigs")
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Spacer()
                    }
                    .padding(.vertical, 8)
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

            Text("No Claude Code sessions detected")
                .font(.headline)
                .foregroundColor(.secondary)

            Text("Start a Claude Code session to see it here.")
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
                Text("Irrlicht")
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
            if sessionManager.isWatching {
                Circle()
                    .fill(Color.green)
                    .frame(width: 6, height: 6)
                Text("watching")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            } else {
                Circle()
                    .fill(Color.red)
                    .frame(width: 6, height: 6)
                Text("not watching")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
        }
    }
    
    // MARK: - Session List

    private var sessionGroups: [SessionGroup] {
        let allSessions = sessionManager.sessions
        let sessionIds = Set(allSessions.map { $0.id })

        // Identify subagent sessions (have a parentSessionId that exists in current sessions)
        let subagentIds: Set<String> = Set(allSessions.compactMap { session in
            guard let pid = session.parentSessionId, sessionIds.contains(pid) else { return nil }
            return session.id
        })

        // Build groups in order of top-level sessions
        return allSessions
            .filter { !subagentIds.contains($0.id) }
            .map { parent in
                let subagents = allSessions.filter { $0.parentSessionId == parent.id }
                return SessionGroup(parent: parent, subagents: subagents)
            }
    }

    var projectGroups: [ProjectGroup] {
        let groups = sessionGroups

        // Group session groups by project name (from git repo root).
        // Sessions in different worktrees of the same repo share the same project name.
        var grouped: [String: [SessionGroup]] = [:]
        for group in groups {
            let project = group.parent.projectName ?? "unknown"
            grouped[project, default: []].append(group)
        }

        // Convert to ProjectGroup array, sorted by project name
        return grouped.map { key, value in
            ProjectGroup(projectDirectory: key, sessionGroups: value)
        }.sorted { $0.projectDirectory < $1.projectDirectory }
    }

    private var sessionListContent: some View {
        let allProjectGroups = projectGroups
        // Pre-compute starting flat index for each project group (for drag/drop continuity)
        var startIndices: [String: Int] = [:]
        var runningIndex = 0
        for pg in allProjectGroups {
            startIndices[pg.id] = runningIndex
            runningIndex += pg.sessionGroups.count
        }
        let totalGroups = runningIndex

        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 1) {
                ForEach(allProjectGroups) { projectGroup in
                    ProjectGroupSectionView(
                        projectGroup: projectGroup,
                        startingGroupIndex: startIndices[projectGroup.id] ?? 0
                    )
                }

                // Drop zone at the end of the list
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
    @State private var isHovered = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                // State dot
                Circle()
                    .fill(Color(hex: session.state.color))
                    .frame(width: 8, height: 8)
                    .accessibilityIdentifier("session-state-icon-\(session.id)")

                // Agent number
                Text("\(agentNumber)")
                    .font(.system(size: 9, weight: .medium, design: .monospaced))
                    .foregroundColor(.secondary)
                    .frame(width: 12, alignment: .trailing)

                // Subagent count badge
                if let subs = session.subagents, subs.total > 0 {
                    Text("\(subs.total)")
                        .font(.system(size: 9, weight: .bold, design: .rounded))
                        .foregroundColor(.white)
                        .frame(width: 14, height: 14)
                        .background(Color(hex: session.state.color))
                        .clipShape(Circle())
                }

                // Branch name (project name is in the group header)
                Text(session.gitBranch ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.primary)
                    .lineLimit(1)

                // Context utilization bar
                if let metrics = session.metrics, metrics.hasContextData {
                    ContextBar(utilization: metrics.contextUtilization,
                               pressureColor: metrics.contextPressureColor)
                        .frame(maxWidth: 80, maxHeight: 8)
                    Text(metrics.formattedContextUtilization)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundColor(Color(hex: metrics.contextPressureColor))
                }

                // Estimated cost
                if let cost = session.metrics?.formattedCost {
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
                    .accessibilityIdentifier("session-model-label-\(session.id)")
                if let icon = session.adapterIcon {
                    Image(nsImage: icon)
                        .frame(width: 12, height: 12)
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

            // Context utilization
            if let metrics = session.metrics, metrics.hasContextData {
                Text(metrics.formattedContextUtilization)
                    .font(.caption2)
                    .foregroundColor(Color(hex: metrics.contextPressureColor))
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

// MARK: - Global Agent Row View

struct GlobalAgentRowView: View {
    let agent: GlobalAgent
    @State private var isHovered = false

    var body: some View {
        HStack(spacing: 6) {
            Text(agent.displayEmoji)
                .font(.system(size: 12))

            Text(agent.role.capitalized)
                .font(.system(.body, design: .monospaced))
                .foregroundColor(.primary)

            Spacer()

            Image(systemName: agent.stateGlyph)
                .font(.system(size: 10))
                .foregroundColor(Color(hex: agent.stateColor))

            Text(agent.state)
                .font(.caption2)
                .foregroundColor(Color(hex: agent.stateColor))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 5)
        .background(isHovered ? Color.accentColor.opacity(0.06) : Color.clear)
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("global-agent-\(agent.role)")
    }
}

// MARK: - Convoy Section View

struct ConvoySectionView: View {
    let convoys: [WorkUnit]
    @State private var isExpanded = true

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Convoy header
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

                    Text("🚚")
                        .font(.system(size: 10))

                    Text("Convoys")
                        .font(.system(.caption, design: .monospaced))
                        .fontWeight(.semibold)
                        .foregroundColor(.secondary)

                    Spacer()
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 4)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if isExpanded {
                ForEach(convoys) { convoy in
                    ConvoyRowView(convoy: convoy)
                }
            }
        }
    }
}

// MARK: - Convoy Row View

struct ConvoyRowView: View {
    let convoy: WorkUnit
    @State private var isHovered = false

    var body: some View {
        HStack(spacing: 8) {
            Spacer().frame(width: 20)

            Text(convoy.name)
                .font(.system(.caption, design: .monospaced))
                .foregroundColor(convoy.isComplete ? .secondary : .primary)

            Spacer()

            // Dot-bar
            Text(convoy.dotBar)
                .font(.system(size: 10, design: .monospaced))
                .foregroundColor(Color(hex: convoy.dotColor))

            // Fraction
            Text(convoy.fractionString)
                .font(.caption2)
                .foregroundColor(.secondary)

            if convoy.isComplete {
                Text("✓")
                    .font(.caption2)
                    .foregroundColor(Color(hex: "#34C759"))
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 3)
        .opacity(convoy.isComplete ? 0.6 : 1.0)
        .background(isHovered ? Color.accentColor.opacity(0.05) : Color.clear)
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("convoy-row-\(convoy.id)")
    }
}

// MARK: - Codebase Row View (replaces RigRowView)

struct CodebaseRowView: View {
    let codebase: GasTownCodebase
    @State private var isExpanded = true
    @State private var isHovered = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Rig header
            HStack(spacing: 6) {
                Image(systemName: isExpanded ? "chevron.down" : "chevron.right")
                    .font(.system(size: 8))
                    .foregroundColor(.secondary)
                    .frame(width: 10)

                Circle()
                    .fill(codebase.isOperational ? Color.green : Color.red)
                    .frame(width: 7, height: 7)

                Text(codebase.rig)
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(.primary)

                Spacer()

                // Agent summary
                if codebase.agentCount > 0 {
                    Text("\(codebase.agentCount) agent\(codebase.agentCount == 1 ? "" : "s")")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(isHovered ? Color.accentColor.opacity(0.06) : Color.clear)
            .contentShape(Rectangle())
            .onTapGesture {
                withAnimation(.easeInOut(duration: 0.15)) {
                    isExpanded.toggle()
                }
            }
            .onHover { hovering in
                withAnimation(.easeInOut(duration: 0.15)) {
                    isHovered = hovering
                }
            }

            // Expanded agent rows
            if isExpanded {
                // Main worktree agents (witness, refinery, crew)
                if let mainWt = codebase.mainWorktree {
                    ForEach(mainWt.safeAgents) { agent in
                        AgentRowView(agent: agent)
                    }
                }

                // Polecat worktrees
                ForEach(codebase.polecatWorktrees) { wt in
                    ForEach(wt.safeAgents) { agent in
                        AgentRowView(agent: agent)
                    }
                }
            }
        }
        .accessibilityIdentifier("codebase-row-\(codebase.rig)")
    }
}

// MARK: - Agent Row View (replaces PolecatRowView)

struct AgentRowView: View {
    let agent: GasTownAgent
    @State private var isHovered = false

    var body: some View {
        HStack(spacing: 6) {
            Spacer().frame(width: 24)

            Text(agent.displayEmoji)
                .font(.system(size: 10))
                .frame(width: 16)

            Text(agent.displayLabel)
                .font(.system(.caption, design: .monospaced))
                .foregroundColor(.primary)

            if let beadId = agent.beadId, !beadId.isEmpty {
                Text(beadId)
                    .font(.caption2)
                    .foregroundColor(.purple)
            }

            Spacer()

            Image(systemName: agent.stateGlyph)
                .font(.system(size: 9))
                .foregroundColor(Color(hex: agent.stateColor))

            Text(agent.state)
                .font(.caption2)
                .foregroundColor(Color(hex: agent.stateColor))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 3)
        .background(isHovered ? Color.accentColor.opacity(0.05) : Color.clear)
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("agent-row-\(agent.id)")
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
    let projectDirectory: String
    let sessionGroups: [SessionGroup]

    var id: String { projectDirectory }
}

// MARK: - Project Group Section View

struct ProjectGroupSectionView: View {
    let projectGroup: ProjectGroup
    let startingGroupIndex: Int
    @EnvironmentObject var sessionManager: SessionManager
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
            // Collapsible project header
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

                    Text(projectGroup.projectDirectory)
                        .font(.system(.caption, design: .monospaced))
                        .fontWeight(.semibold)
                        .foregroundColor(projectNameColor)

                    if let cost = formattedTotalCost {
                        Text(cost)
                            .font(.system(size: 9, weight: .medium, design: .monospaced))
                            .foregroundColor(.secondary)
                    }

                    Spacer()

                    let count = projectGroup.sessionGroups.count
                    Text("\(count) \(count == 1 ? "session" : "sessions")")
                        .font(.caption2)
                        .foregroundColor(.secondary.opacity(0.7))
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 4)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            // Session rows (indented under the project header)
            if isExpanded {
                ForEach(Array(projectGroup.sessionGroups.enumerated()), id: \.element.id) { localIndex, group in
                    SessionRowView(session: group.parent, agentNumber: localIndex + 1)
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

                    ForEach(group.subagents) { subagent in
                        SubagentRowView(session: subagent)
                            .padding(.leading, 8)
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
                            contextUtilization: 7.5,
                            pressureLevel: "safe",
                            estimatedCostUSD: nil
                        )
                    )
                ]
                return manager
            }())
    }
}