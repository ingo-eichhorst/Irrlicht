import SwiftUI

struct SessionListView: View {
    @EnvironmentObject var sessionManager: SessionManager
    @State private var isQuitButtonHovered = false
    
    var body: some View {
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
            
            // Quit button at bottom
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
    
    // MARK: - Empty State
    
    private var emptyStateView: some View {
        VStack(spacing: 8) {
            Image(systemName: "lightbulb.slash")
                .font(.system(size: 24))
                .foregroundColor(.secondary)
            
            Text("No Claude Code sessions detected")
                .font(.headline)
                .foregroundColor(.secondary)
            
            Text("Start a Claude Code session to see it here")
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
    
    private var sessionListContent: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 1) {
                ForEach(sessionManager.sessions.indices, id: \.self) { index in
                    SessionRowView(session: sessionManager.sessions[index])
                        .contentShape(Rectangle())
                        .onTapGesture {
                            // TODO: Handle session selection in Phase 6
                            print("Selected session: \(sessionManager.sessions[index].id)")
                        }
                        .onDrag {
                            return NSItemProvider(object: sessionManager.sessions[index].id as NSString)
                        }
                        .onDrop(of: [.text], delegate: SessionDropDelegate(
                            sessionManager: sessionManager,
                            targetIndex: index
                        ))
                }
                
                // Drop zone at the end of the list
                Rectangle()
                    .fill(Color.clear)
                    .frame(height: 20)
                    .onDrop(of: [.text], delegate: SessionDropDelegate(
                        sessionManager: sessionManager,
                        targetIndex: sessionManager.sessions.count
                    ))
            }
        }
        .frame(maxHeight: 400) // Limit height for scrolling
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
                    
                    Spacer()
                    
                    if session.safeEventCount > 0 {
                        Text("\(session.safeEventCount) events")
                            .font(.caption2)
                            .foregroundColor(Color.secondary.opacity(0.7))
                    }
                }
                
                // Show metrics if available
                if let metrics = session.metrics {
                    HStack(spacing: 8) {
                        if metrics.messagesPerMinute > 0 {
                            Label(metrics.formattedMessagesPerMinute, systemImage: "chart.line.uptrend.xyaxis")
                                .font(.caption2)
                                .foregroundColor(.primary)
                        }
                        
                        if metrics.elapsedSeconds > 0 {
                            Label(metrics.formattedElapsedTime, systemImage: "clock")
                                .font(.caption2)
                                .foregroundColor(.primary)
                        }
                        
                        // Context utilization indicator
                        if metrics.hasContextData {
                            HStack(spacing: 2) {
                                Text(metrics.contextPressureIcon)
                                    .font(.caption2)
                                Text(metrics.formattedContextUtilization)
                                    .font(.caption2)
                                    .foregroundColor(Color(hex: metrics.contextPressureColor))
                            }
                        }
                        
                        // Token count indicator
                        if metrics.totalTokens > 0 {
                            Label(metrics.formattedTokenCount, systemImage: "textformat.abc")
                                .font(.caption2)
                                .foregroundColor(.primary)
                        }
                        
                        Spacer()
                    }
                    .padding(.top, 1)
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
            .help("Reset to finished state")
            
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

struct SessionDropDelegate: DropDelegate {
    let sessionManager: SessionManager
    let targetIndex: Int
    
    func validateDrop(info: DropInfo) -> Bool {
        return info.hasItemsConforming(to: [.text])
    }
    
    func dropEntered(info: DropInfo) {
        // Visual feedback could be added here
    }
    
    func dropExited(info: DropInfo) {
        // Clear visual feedback
    }
    
    func performDrop(info: DropInfo) -> Bool {
        guard let itemProvider = info.itemProviders(for: [.text]).first else {
            return false
        }
        
        itemProvider.loadObject(ofClass: NSString.self) { item, error in
            guard let sessionId = item as? String else {
                return
            }
            
            DispatchQueue.main.async {
                // Find the source index on the main thread
                guard let sourceIndex = sessionManager.sessions.firstIndex(where: { $0.id == sessionId }) else {
                    return
                }
                
                sessionManager.reorderSession(from: sourceIndex, to: targetIndex)
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
                        updatedAt: Date().addingTimeInterval(-60),
                        eventCount: 5,
                        lastEvent: "UserPromptSubmit",
                        metrics: SessionMetrics(
                            messagesPerMinute: 2.5,
                            elapsedSeconds: 180,
                            lastMessageAt: Date().addingTimeInterval(-30),
                            sessionStartAt: Date().addingTimeInterval(-180),
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
                        updatedAt: Date().addingTimeInterval(-300),
                        eventCount: 12,
                        lastEvent: "Notification",
                        metrics: SessionMetrics(
                            messagesPerMinute: 1.8,
                            elapsedSeconds: 420,
                            lastMessageAt: Date().addingTimeInterval(-90),
                            sessionStartAt: Date().addingTimeInterval(-420),
                            totalTokens: 85000,
                            modelName: "claude-3-haiku",
                            contextUtilization: 42.5,
                            pressureLevel: "caution"
                        )
                    ),
                    SessionState(
                        id: "sess_old456finished",
                        state: .finished,
                        model: "claude-3-opus",
                        cwd: "/Users/user/projects/another-project",
                        transcriptPath: "/Users/user/.claude/projects/completed/transcript.jsonl", 
                        gitBranch: "main",
                        projectName: "another-project",
                        updatedAt: Date().addingTimeInterval(-1800),
                        eventCount: 8,
                        lastEvent: "SessionEnd",
                        metrics: SessionMetrics(
                            messagesPerMinute: 0.0,
                            elapsedSeconds: 1200,
                            lastMessageAt: Date().addingTimeInterval(-1800),
                            sessionStartAt: Date().addingTimeInterval(-3000),
                            totalTokens: 175000,
                            modelName: "claude-3-opus",
                            contextUtilization: 87.5,
                            pressureLevel: "warning"
                        )
                    )
                ]
                
                // Assign duplicate indexes like the real SessionManager would
                manager.sessions = mockSessions
                return manager
            }())
    }
}