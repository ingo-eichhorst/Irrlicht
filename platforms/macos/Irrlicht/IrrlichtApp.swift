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
    @ObservedObject var sessionManager: SessionManager
    @ObservedObject var gasTownProvider: GasTownProvider

    private var gasTownAgentCount: Int {
        sessionManager.sessions.filter { gasTownProvider.ownsSession($0) }.count
    }

    var body: some View {
        if let img = buildCombinedImage() {
            Image(nsImage: img)
        } else if sessionManager.sessions.isEmpty && !gasTownProvider.isDaemonRunning {
            Image(systemName: "sparkle")
                .font(.system(size: 14))
                .foregroundColor(.white)
        } else {
            Image(systemName: "sparkle")
                .font(.system(size: 14))
        }
    }

    private func buildCombinedImage() -> NSImage? {
        let dotsImage = MenuBarStatusRenderer.buildStatusImage(
            sessions: sessionManager.sessions,
            projectGroupOrder: sessionManager.projectGroupOrder
        )

        // No Gas Town → just return session dots as before.
        guard gasTownProvider.isDaemonRunning else { return dotsImage }

        // Build the "⛽N" badge text.
        let count = gasTownAgentCount
        let emoji = NSAttributedString(string: "\u{26FD}", attributes: [
            .font: NSFont.systemFont(ofSize: 12)
        ])
        let countStr = NSAttributedString(string: "\(count > 0 ? "\(count)" : "")", attributes: [
            .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .bold),
            .foregroundColor: NSColor.white
        ])
        let badge = NSMutableAttributedString()
        badge.append(emoji)
        badge.append(countStr)
        let badgeSize = badge.size()

        let gap: CGFloat = dotsImage != nil ? 4 : 0
        let dotsWidth = dotsImage?.size.width ?? 0
        let dotsHeight = dotsImage?.size.height ?? 0
        let totalWidth = badgeSize.width + gap + dotsWidth
        let totalHeight = max(badgeSize.height, dotsHeight)

        let combined = NSImage(size: NSSize(width: totalWidth, height: totalHeight))
        combined.lockFocus()
        let badgeY = (totalHeight - badgeSize.height) / 2
        badge.draw(at: NSPoint(x: 0, y: badgeY))
        if let dotsImage {
            let dotsY = (totalHeight - dotsHeight) / 2
            dotsImage.draw(at: NSPoint(x: badgeSize.width + gap, y: dotsY),
                           from: .zero, operation: .sourceOver, fraction: 1)
        }
        combined.unlockFocus()
        combined.isTemplate = false
        return combined
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    var daemonManager: DaemonManager?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Try Bundle.module (SwiftPM resource bundle) first, then Bundle.main (.app bundle)
        let iconURL = Bundle.module.url(forResource: "AppIcon", withExtension: "icns", subdirectory: "Resources")
            ?? Bundle.main.url(forResource: "AppIcon", withExtension: "icns")
        if let iconURL, let icon = NSImage(contentsOf: iconURL) {
            NSApp.applicationIconImage = icon
        }
    }

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
        } label: {
            StatusIndicatorLabel(
                    sessionManager: sessionManager,
                    gasTownProvider: gasTownProvider
                )
                .task {
                    // Wire gasTownProvider before daemon starts (hydration needs it).
                    sessionManager.gasTownProvider = gasTownProvider
                    appDelegate.daemonManager = daemonManager
                    daemonManager.start()
                }
        }
        .menuBarExtraStyle(.window)
    }
}
