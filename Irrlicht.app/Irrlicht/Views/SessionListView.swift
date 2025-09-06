import SwiftUI

struct SessionListView: View {
    @EnvironmentObject var sessionManager: SessionManager
    
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
                
                Text(sessionManager.glyphStrip)
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(.primary)
            }
            
            Spacer()
            
            statusIndicator
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
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
                ForEach(sessionManager.sessions) { session in
                    SessionRowView(session: session)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            // TODO: Handle session selection in Phase 6
                            print("Selected session: \(session.id)")
                        }
                }
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
            Text(session.state.glyph)
                .font(.system(.body, design: .monospaced))
                .foregroundColor(Color(hex: session.state.color))
                .frame(width: 16)
            
            VStack(alignment: .leading, spacing: 2) {
                HStack {
                    Text(session.shortId)
                        .font(.system(.body, design: .monospaced))
                        .foregroundColor(.primary)
                    
                    Text("·")
                        .foregroundColor(.secondary)
                    
                    Text(session.state.rawValue)
                        .font(.body)
                        .foregroundColor(Color(hex: session.state.color))
                    
                    Spacer()
                    
                    Text(session.timeAgo)
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                
                HStack {
                    Text(session.model)
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
                                .foregroundColor(.blue)
                        }
                        
                        if metrics.elapsedSeconds > 0 {
                            Label(metrics.formattedElapsedTime, systemImage: "clock")
                                .font(.caption2)
                                .foregroundColor(.green)
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
            .environmentObject({
                let manager = SessionManager()
                // Add some mock sessions for preview
                manager.sessions = [
                    SessionState(
                        id: "sess_abc123def456",
                        state: .working,
                        model: "claude-3.7-sonnet", 
                        cwd: "/Users/user/projects/test",
                        transcriptPath: "/Users/user/.claude/projects/test/transcript.jsonl",
                        updatedAt: Date().addingTimeInterval(-60),
                        eventCount: 5,
                        lastEvent: "UserPromptSubmit"
                    ),
                    SessionState(
                        id: "sess_xyz789ghi012",
                        state: .waiting,
                        model: "claude-3-haiku",
                        cwd: "/Users/user/projects/another", 
                        transcriptPath: "/Users/user/.claude/projects/another/transcript.jsonl",
                        updatedAt: Date().addingTimeInterval(-300),
                        eventCount: 12,
                        lastEvent: "Notification"
                    ),
                    SessionState(
                        id: "sess_old456finished",
                        state: .finished,
                        model: "claude-3-opus",
                        cwd: "/Users/user/projects/completed",
                        transcriptPath: "/Users/user/.claude/projects/completed/transcript.jsonl", 
                        updatedAt: Date().addingTimeInterval(-1800),
                        eventCount: 8,
                        lastEvent: "SessionEnd"
                    )
                ]
                return manager
            }())
    }
}