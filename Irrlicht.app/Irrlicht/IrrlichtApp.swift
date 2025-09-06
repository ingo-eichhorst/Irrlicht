import SwiftUI

struct StatusIndicatorLabel: View {
    let sessions: [SessionState]
    
    var body: some View {
        if sessions.isEmpty {
            // No sessions - show dimmed lightbulb
            Image(systemName: "lightbulb")
                .foregroundColor(.secondary)
        } else {
            // Show individual status indicators
            HStack(spacing: spacingForCount) {
                ForEach(sessions.prefix(maxDisplayCount), id: \.id) { session in
                    Text(session.state.emoji)
                        .font(.system(size: fontSizeForCount))
                }
                
                // Show overflow indicator if too many sessions
                if sessions.count > maxDisplayCount {
                    Text("…")
                        .font(.system(size: fontSizeForCount))
                        .foregroundColor(.secondary)
                }
            }
        }
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