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
    let projectGroupOrder: [String]

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
        MenuBarStatusRenderer.buildStatusImage(
            sessions: sessions,
            projectGroupOrder: projectGroupOrder
        )
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
            StatusIndicatorLabel(sessions: sessionManager.sessions, projectGroupOrder: sessionManager.projectGroupOrder)
                .task {
                    // Start daemon on app launch (label renders immediately, unlike popover content).
                    appDelegate.daemonManager = daemonManager
                    daemonManager.start()
                }
        }
        .menuBarExtraStyle(.window)
    }
}
