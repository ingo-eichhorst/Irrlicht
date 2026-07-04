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
    let updateManager = UpdateManager()

    private var menuBarController: MenuBarController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // LSUIElement apps have no visible menu bar, but NSApp still routes key
        // equivalents through NSApp.mainMenu. Without an Edit menu, Cmd+V/X/C/Z/A
        // don't reach text fields. Set one up invisibly so standard text-editing
        // shortcuts work in SettingsView.
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: #selector(UndoManager.undo), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: #selector(UndoManager.redo), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut",        action: #selector(NSText.cut(_:)),       keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy",       action: #selector(NSText.copy(_:)),      keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste",      action: #selector(NSText.paste(_:)),     keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        let editItem = NSMenuItem()
        editItem.submenu = editMenu
        let mainMenu = NSMenu()
        mainMenu.addItem(editItem)
        NSApp.mainMenu = mainMenu

        let iconURL = Bundle.main.url(forResource: "AppIcon", withExtension: "icns")
        if let iconURL, let icon = NSImage(contentsOf: iconURL) {
            NSApp.applicationIconImage = icon
        }

        // Create status item + panel before wiring so the first Combine
        // tick has a target to render into.
        menuBarController = MenuBarController(
            daemonManager: daemonManager,
            sessionManager: sessionManager,
            gasTownProvider: gasTownProvider,
            updateManager: updateManager
        )

        // Wire gasTownProvider before daemon starts (hydration needs it).
        sessionManager.gasTownProvider = gasTownProvider
        daemonManager.start()
        LoginItemManager.reconcileOnLaunch()
    }

    func applicationWillTerminate(_ notification: Notification) {
        daemonManager.stop()
    }
}
