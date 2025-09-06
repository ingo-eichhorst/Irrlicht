import SwiftUI

struct StatusIndicatorLabel: View {
    let sessions: [SessionState]
    
    var body: some View {
        if sessions.isEmpty {
            // No sessions - show dimmed lightbulb
            Image(systemName: "lightbulb")
                .foregroundColor(.secondary)
        } else {
            // Show individual status indicators as concatenated text
            Text(statusDisplayString)
                .font(.system(size: fontSizeForCount))
        }
    }
    
    // Create concatenated emoji string with spacing
    private var statusDisplayString: String {
        let displaySessions = sessions.prefix(maxDisplayCount)
        let emojiArray = displaySessions.map { $0.state.emoji }
        
        // Add overflow indicator if needed
        var finalEmojis = emojiArray
        if sessions.count > maxDisplayCount {
            finalEmojis.append("…")
        }
        
        // Join with spaces based on session count for visual clarity
        let separator = spacingForCount > 1 ? " " : ""
        return finalEmojis.joined(separator: separator)
    }
    
    // Dynamic sizing based on session count
    private var maxDisplayCount: Int {
        switch sessions.count {
        case 0...6: return sessions.count
        default: return 5  // Show first 5 + overflow indicator
        }
    }
    
    private var fontSizeForCount: CGFloat {
        switch sessions.count {
        case 0...2: return 14  // Large
        case 3...4: return 12  // Medium  
        case 5...6: return 10  // Small
        default: return 8      // Mini
        }
    }
    
    private var spacingForCount: CGFloat {
        switch sessions.count {
        case 0...2: return 3
        case 3...4: return 2
        default: return 1
        }
    }
}

@main
struct IrrlichtApp: App {
    @StateObject private var sessionManager = SessionManager()
    
    var body: some Scene {
        MenuBarExtra {
            SessionListView()
                .environmentObject(sessionManager)
        } label: {
            StatusIndicatorLabel(sessions: sessionManager.sessions)
        }
        .menuBarExtraStyle(.window)
    }
}