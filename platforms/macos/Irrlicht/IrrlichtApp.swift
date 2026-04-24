import AppKit
import Foundation

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

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let daemonManager = DaemonManager()
    let sessionManager = SessionManager()
    let gasTownProvider = GasTownProvider()

    private var menuBarController: MenuBarController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Try Bundle.module (SwiftPM resource bundle) first, then Bundle.main (.app bundle)
        let iconURL = Bundle.module.url(forResource: "AppIcon", withExtension: "icns", subdirectory: "Resources")
            ?? Bundle.main.url(forResource: "AppIcon", withExtension: "icns")
        if let iconURL, let icon = NSImage(contentsOf: iconURL) {
            NSApp.applicationIconImage = icon
        }

        // Create status item + panel before wiring so the first Combine
        // tick has a target to render into.
        menuBarController = MenuBarController(
            daemonManager: daemonManager,
            sessionManager: sessionManager,
            gasTownProvider: gasTownProvider
        )

        // Wire gasTownProvider before daemon starts (hydration needs it).
        sessionManager.gasTownProvider = gasTownProvider
        daemonManager.start()
    }

    func applicationWillTerminate(_ notification: Notification) {
        daemonManager.stop()
    }
}
