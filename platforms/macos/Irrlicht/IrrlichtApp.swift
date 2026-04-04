import SwiftUI

struct StatusIndicatorLabel: View {
    let sessions: [SessionState]

    var body: some View {
        if sessions.isEmpty {
            Image(systemName: "sparkle")
                .font(.system(size: 14))
                .foregroundColor(.white)
        } else if let img = buildStatusImage() {
            Image(nsImage: img)
        } else {
            Image(systemName: "sparkle")
                .font(.system(size: 14))
        }
    }

    private func buildStatusImage() -> NSImage? {
        // Group sessions by project
        var groups: [(String, [SessionState])] = []
        var seen: [String: Int] = [:]
        let capped = Array(sessions.filter { $0.parentSessionId == nil }.prefix(8))
        for s in capped {
            let key = s.projectName ?? s.cwd
            if let idx = seen[key] {
                groups[idx].1.append(s)
            } else {
                seen[key] = groups.count
                groups.append((key, [s]))
            }
        }

        let r: CGFloat = 5          // circle radius
        let overlap: CGFloat = 4    // overlap within group
        let groupGap: CGFloat = 6   // gap between groups
        let height: CGFloat = 18
        let cy = height / 2

        // Calculate total width
        var totalWidth: CGFloat = 0
        for (i, (_, group)) in groups.enumerated() {
            if i > 0 { totalWidth += groupGap }
            totalWidth += CGFloat(group.count) * (r * 2 - overlap) + overlap
        }

        // Build SVG
        var svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(totalWidth))" height="\(Int(height))">
        """

        var x: CGFloat = r
        for (i, (_, group)) in groups.enumerated() {
            if i > 0 { x += groupGap }
            for s in group {
                let hex = s.state.color.trimmingCharacters(in: CharacterSet(charactersIn: "#"))
                svg += """
                <circle cx="\(Int(x))" cy="\(Int(cy))" r="\(Int(r))" fill="#\(hex)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
                """
                x += r * 2 - overlap
            }
        }

        svg += "</svg>"

        guard let data = svg.data(using: .utf8),
              let nsImage = NSImage(data: data) else { return nil }

        nsImage.isTemplate = false
        nsImage.size = NSSize(width: totalWidth, height: height)
        return nsImage
    }
}

@main
struct IrrlichtApp: App {
    @StateObject private var daemonManager = DaemonManager()
    @StateObject private var sessionManager = SessionManager()
    @StateObject private var gasTownProvider = GasTownProvider()

    var body: some Scene {
        MenuBarExtra {
            SessionListView()
                .environmentObject(daemonManager)
                .environmentObject(sessionManager)
                .environmentObject(gasTownProvider)
                .onAppear {
                    // Wire gasTownProvider to sessionManager for WebSocket forwarding.
                    sessionManager.gasTownProvider = gasTownProvider
                    // Start the embedded daemon (or detect an external one).
                    daemonManager.start()
                }
        } label: {
            StatusIndicatorLabel(sessions: sessionManager.sessions)
        }
        .menuBarExtraStyle(.window)
    }
}