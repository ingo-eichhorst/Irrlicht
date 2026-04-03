import SwiftUI

struct StatusIndicatorLabel: View {
    let sessions: [SessionState]

    var body: some View {
        if sessions.isEmpty {
            Image(systemName: "sparkle")
                .font(.system(size: 14))
                .foregroundColor(.white)
        } else {
            // Menu bar labels only reliably render Text/Image.
            // Group emojis by project: tight within group, space between groups.
            Text(groupedDisplayString)
                .font(.system(size: fontSizeForCount))
        }
    }

    private var groupedDisplayString: String {
        var groups: [(String, [SessionState])] = []
        var seen: [String: Int] = [:]
        let capped = Array(sessions.prefix(maxDisplayCount))
        for s in capped {
            let key = s.projectName ?? s.cwd
            if let idx = seen[key] {
                groups[idx].1.append(s)
            } else {
                seen[key] = groups.count
                groups.append((key, [s]))
            }
        }
        // Within a group: no separator. Between groups: space.
        let parts = groups.map { _, group in
            group.map { $0.state.emoji }.joined()
        }
        var result = parts.joined(separator: " ")
        if sessions.count > maxDisplayCount {
            result += "…"
        }
        return result
    }

    private var maxDisplayCount: Int {
        switch sessions.count {
        case 0...6: return sessions.count
        default: return 5
        }
    }

    private var fontSizeForCount: CGFloat {
        switch sessions.count {
        case 0...2: return 14
        case 3...4: return 12
        case 5...6: return 10
        default: return 8
        }
    }
}

@main
struct IrrlichtApp: App {
    @StateObject private var sessionManager = SessionManager()
    @StateObject private var gasTownProvider = GasTownProvider()

    var body: some Scene {
        MenuBarExtra {
            SessionListView()
                .environmentObject(sessionManager)
                .environmentObject(gasTownProvider)
                .onAppear {
                    // Wire gasTownProvider to sessionManager for WebSocket forwarding.
                    sessionManager.gasTownProvider = gasTownProvider
                }
        } label: {
            StatusIndicatorLabel(sessions: sessionManager.sessions)
        }
        .menuBarExtraStyle(.window)
    }
}