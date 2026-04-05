import SwiftUI

let appVersion: String = {
    if let v = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String { return v }
    // Fallback: read version.json from repo root (useful for debug builds)
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent() // Irrlicht/
        .deletingLastPathComponent() // macos/
        .deletingLastPathComponent() // platforms/
        .deletingLastPathComponent() // repo root
        .appendingPathComponent("version.json")
    if let data = try? Data(contentsOf: url),
       let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
       let v = json["version"] as? String { return v }
    return "dev"
}()

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
        // Group sessions by project (top-level only)
        var groups: [(String, [SessionState])] = []
        var seen: [String: Int] = [:]
        for s in sessions where s.parentSessionId == nil {
            let key = s.projectName ?? s.cwd
            if let idx = seen[key] {
                groups[idx].1.append(s)
            } else {
                seen[key] = groups.count
                groups.append((key, [s]))
            }
        }

        let r: CGFloat = 5
        let overlap: CGFloat = 4
        let groupGap: CGFloat = 6
        let height: CGFloat = 18
        let cy = height / 2

        // Pre-compute SVG elements for each group
        struct GroupRender {
            var elements: String
            var width: CGFloat
        }

        var renders: [GroupRender] = []
        for (_, groupSessions) in groups.prefix(8) {
            if groupSessions.count <= 3 {
                // ≤3: individual overlapping filled circles
                var el = ""
                var lx: CGFloat = r
                for s in groupSessions {
                    let hex = s.state.hexColor
                    el += """
                    <circle cx="\(Int(lx))" cy="\(Int(cy))" r="\(Int(r))" fill="#\(hex)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
                    """
                    lx += r * 2 - overlap
                }
                let w = CGFloat(groupSessions.count) * (r * 2 - overlap) + overlap
                renders.append(GroupRender(elements: el, width: w))
            } else {
                // >3: single filled circle (dominant state) + total count
                let hex = SessionState.State.dominant(in: groupSessions.map(\.state)).hexColor
                let count = groupSessions.count
                let fontSize: CGFloat = 10
                let textX = Int(r * 2 + 2)
                let textY = Int(cy + fontSize * 0.35)
                let countStr = "\(count)"
                let textWidth: CGFloat = CGFloat(countStr.count) * 6.5
                let el = """
                <circle cx="\(Int(r))" cy="\(Int(cy))" r="\(Int(r))" fill="#\(hex)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>
                <text x="\(textX)" y="\(textY)" font-family="Menlo,monospace" font-size="\(Int(fontSize))" font-weight="bold" fill="#\(hex)">\(countStr)</text>
                """
                renders.append(GroupRender(elements: el, width: r * 2 + 2 + textWidth))
            }
        }

        // Calculate total width
        var totalWidth: CGFloat = 0
        for (i, render) in renders.enumerated() {
            if i > 0 { totalWidth += groupGap }
            totalWidth += render.width
        }
        guard totalWidth > 0 else { return nil }

        // Assemble SVG
        var svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(Int(totalWidth))" height="\(Int(height))">
        """
        var offsetX: CGFloat = 0
        for (i, render) in renders.enumerated() {
            if i > 0 { offsetX += groupGap }
            svg += "<g transform=\"translate(\(Int(offsetX)),0)\">\(render.elements)</g>"
            offsetX += render.width
        }
        svg += "</svg>"

        guard let data = svg.data(using: .utf8),
              let nsImage = NSImage(data: data) else { return nil }

        nsImage.isTemplate = false
        nsImage.size = NSSize(width: totalWidth, height: height)
        return nsImage
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    var daemonManager: DaemonManager?

    func applicationWillTerminate(_ notification: Notification) {
        daemonManager?.stop()
    }
}

@main
struct IrrlichtApp: App {
    @NSApplicationDelegateAdaptor private var appDelegate: AppDelegate
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
                }
        } label: {
            StatusIndicatorLabel(sessions: sessionManager.sessions)
                .task {
                    // Start daemon on app launch (label renders immediately, unlike popover content).
                    appDelegate.daemonManager = daemonManager
                    daemonManager.start()
                }
        }
        .menuBarExtraStyle(.window)
    }
}