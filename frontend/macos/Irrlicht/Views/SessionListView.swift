import SwiftUI

struct SessionListView: View {
    @EnvironmentObject var sessionManager: SessionManager
    @State private var isQuitButtonHovered = false
    @State private var isSettingsButtonHovered = false
    @State private var showSettings = false
    
    var body: some View {
        if showSettings {
            SettingsView(isPresented: $showSettings)
        } else {
            VStack(alignment: .leading, spacing: 0) {
                if sessionManager.sessions.isEmpty {
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

    private var sessionListContent: some View {
        let groups = sessionGroups
        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 1) {
                ForEach(Array(groups.enumerated()), id: \.element.id) { groupIndex, group in
                    // Parent row
                    SessionRowView(session: group.parent)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            print("Selected session: \(group.parent.id)")
                        }
                        .onDrag {
                            NSItemProvider(object: group.parent.id as NSString)
                        }
                        .onDrop(of: [.text], delegate: SessionGroupDropDelegate(
                            sessionManager: sessionManager,
                            targetGroupIndex: groupIndex
                        ))

                    // Subagent rows (indented, compact)
                    ForEach(group.subagents) { subagent in
                        SubagentRowView(session: subagent)
                    }
                }

                // Drop zone at the end of the list
                Rectangle()
                    .fill(Color.clear)
                    .frame(height: 20)
                    .onDrop(of: [.text], delegate: SessionGroupDropDelegate(
                        sessionManager: sessionManager,
                        targetGroupIndex: groups.count
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

struct SessionRowView: View {
    let session: SessionState
    @State private var isHovered = false
    
    var body: some View {
        HStack(spacing: 8) {
            // State glyph
            Image(systemName: session.state.glyph)
                .font(.system(.body))
                .foregroundColor(Color(hex: session.state.color))
                .frame(width: 16)
                .accessibilityIdentifier("session-state-icon-\(session.id)")
                .accessibilityLabel("\(session.state.rawValue) state")
            
            VStack(alignment: .leading, spacing: 2) {
                HStack {
                    Text(session.projectName ?? "unknown")
                        .font(.system(.body, design: .monospaced))
                        .foregroundColor(.primary)
                    
                    Text("·")
                        .foregroundColor(.secondary)
                    
                    Text(session.state.rawValue)
                        .font(.body)
                        .foregroundColor(Color(hex: session.state.color))
                    
                    Spacer()
                    
                    // Show action buttons on hover
                    if isHovered {
                        SessionActionButtons(session: session)
                    }
                    
                    TimelineView(.periodic(from: .now, by: 1.0)) { timeline in
                        let formatter = RelativeDateTimeFormatter()
                        formatter.unitsStyle = .abbreviated
                        return Text(formatter.localizedString(for: session.updatedAt, relativeTo: timeline.date))
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
                
                if let branch = session.gitBranch {
                    HStack {
                        Text("(\(branch))")
                            .font(.caption2)
                            .foregroundColor(.secondary.opacity(0.8))
                        
                        Spacer()
                    }
                }
                
                HStack {
                    Text(session.effectiveModel)
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .accessibilityIdentifier("session-model-label-\(session.id)")
                        .accessibilityLabel("model \(session.effectiveModel)")

                    Spacer()

                    if session.safeEventCount > 0 {
                        Text("\(session.safeEventCount) events")
                            .font(.caption2)
                            .foregroundColor(Color.secondary.opacity(0.7))
                    }
                }
                
                // Show metrics if available
                if let metrics = session.metrics {
                    let isActive = session.state == .working || session.state == .waiting
                    TimelineView(.periodic(from: .now, by: 1.0)) { timeline in
                        let _ = timeline.date // Force refresh every second
                        HStack(spacing: 0) {
                            // Elapsed time (left-aligned)
                            HStack {
                                if isActive {
                                    let elapsedSeconds = Int64(Date().timeIntervalSince(session.firstSeen))
                                    if elapsedSeconds > 0 {
                                        Label(metrics.formattedRealtimeElapsedTime(sessionFirstSeen: session.firstSeen), systemImage: "clock")
                                            .font(.caption2)
                                            .foregroundColor(.primary)
                                    }
                                } else {
                                    // For ready sessions, use stored elapsed time
                                    if metrics.elapsedSeconds > 0 {
                                        Label(metrics.formattedElapsedTime, systemImage: "clock")
                                            .font(.caption2)
                                            .foregroundColor(.primary)
                                    }
                                }
                                Spacer()
                            }
                            .frame(minWidth: 70, alignment: .leading)

                            Spacer()

                            // Context utilization (center)
                            HStack(spacing: 2) {
                                if metrics.hasContextData {
                                    Text(metrics.contextPressureIcon)
                                        .font(.caption2)
                                    Text(metrics.formattedContextUtilization)
                                        .font(.caption2)
                                        .foregroundColor(Color(hex: metrics.contextPressureColor))
                                }
                            }
                            .frame(minWidth: 50)

                            Spacer()

                            // Token count (right-aligned)
                            HStack {
                                Spacer()
                                if metrics.totalTokens > 0 {
                                    Label(metrics.formattedTokenCount, systemImage: "textformat.abc")
                                        .font(.caption2)
                                        .foregroundColor(.primary)
                                }
                            }
                            .frame(minWidth: 60, alignment: .trailing)
                        }
                        .padding(.top, 1)
                    }
                    .accessibilityIdentifier("session-context-bar-\(session.id)")
                    .accessibilityLabel(metrics.hasContextData ? "context \(metrics.formattedContextUtilization)" : "no context data")
                }

                // Context pressure alert banner (visible at 80%+ for active sessions)
                if let metrics = session.metrics,
                   session.state == .working || session.state == .waiting,
                   metrics.contextUtilization >= 80 {
                    let isCritical = metrics.contextUtilization >= 95
                    HStack(spacing: 4) {
                        Image(systemName: isCritical ? "exclamationmark.triangle.fill" : "exclamationmark.triangle")
                            .font(.caption2)
                            .foregroundColor(isCritical ? .red : .orange)
                        Text("Switch to a fresh session soon")
                            .font(.caption2)
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
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(isHovered ? Color.accentColor.opacity(0.1) : Color.clear)
        .onHover { hovering in
            withAnimation(.easeInOut(duration: 0.15)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("session-card-\(session.id)")
        .accessibilityLabel("\(session.projectName ?? "unknown") \(session.state.rawValue) \(session.effectiveModel)")
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

// MARK: - Session Group

struct SessionGroup: Identifiable {
    let parent: SessionState
    let subagents: [SessionState]

    var id: String { parent.id }
    var allSessions: [SessionState] { [parent] + subagents }
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
            .environmentObject({
                let manager = SessionManager()
                // Add some mock sessions for preview
                let mockSessions = [
                    SessionState(
                        id: "sess_abc123def456",
                        state: .working,
                        model: "claude-3.7-sonnet", 
                        cwd: "/Users/user/projects/multi-cc-bar",
                        transcriptPath: "/Users/user/.claude/projects/test/transcript.jsonl",
                        gitBranch: "main",
                        projectName: "multi-cc-bar",
                        firstSeen: Date().addingTimeInterval(-180),
                        updatedAt: Date().addingTimeInterval(-60),
                        eventCount: 5,
                        lastEvent: "UserPromptSubmit",
                        metrics: SessionMetrics(
                            elapsedSeconds: 180,
                            totalTokens: 15000,
                            modelName: "claude-3.7-sonnet",
                            contextUtilization: 7.5,
                            pressureLevel: "safe"
                        )
                    ),
                    SessionState(
                        id: "sess_xyz789ghi012",
                        state: .waiting,
                        model: "claude-3-haiku",
                        cwd: "/Users/user/projects/multi-cc-bar", 
                        transcriptPath: "/Users/user/.claude/projects/another/transcript.jsonl",
                        gitBranch: "feature/ui-updates",
                        projectName: "multi-cc-bar",
                        firstSeen: Date().addingTimeInterval(-420),
                        updatedAt: Date().addingTimeInterval(-300),
                        eventCount: 12,
                        lastEvent: "Notification",
                        metrics: SessionMetrics(
                            elapsedSeconds: 420,
                            totalTokens: 85000,
                            modelName: "claude-3-haiku",
                            contextUtilization: 42.5,
                            pressureLevel: "caution"
                        )
                    ),
                    SessionState(
                        id: "sess_old456ready",
                        state: .ready,
                        model: "claude-3-opus",
                        cwd: "/Users/user/projects/another-project",
                        transcriptPath: "/Users/user/.claude/projects/completed/transcript.jsonl", 
                        gitBranch: "main",
                        projectName: "another-project",
                        firstSeen: Date().addingTimeInterval(-3000),
                        updatedAt: Date().addingTimeInterval(-1800),
                        eventCount: 8,
                        lastEvent: "SessionEnd",
                        metrics: SessionMetrics(
                            elapsedSeconds: 1200,
                            totalTokens: 175000,
                            modelName: "claude-3-opus",
                            contextUtilization: 87.5,
                            pressureLevel: "warning"
                        )
                    )
                ]
                
                // Assign duplicate indexes like the real SessionManager would
                manager.sessions = mockSessions + [
                    SessionState(
                        id: "sess_sub001agent",
                        state: .working,
                        model: "claude-3.7-sonnet",
                        cwd: "/Users/user/projects/multi-cc-bar",
                        gitBranch: "main",
                        projectName: "multi-cc-bar",
                        firstSeen: Date().addingTimeInterval(-90),
                        updatedAt: Date().addingTimeInterval(-10),
                        eventCount: 3,
                        lastEvent: "UserPromptSubmit",
                        metrics: SessionMetrics(
                            elapsedSeconds: 90,
                            totalTokens: 8000,
                            modelName: "claude-3.7-sonnet",
                            contextUtilization: 12.3,
                            pressureLevel: "safe"
                        ),
                        parentSessionId: "sess_abc123def456"
                    )
                ]
                return manager
            }())
    }
}