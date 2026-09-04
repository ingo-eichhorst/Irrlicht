import AppKit
import ApplicationServices
import DesktopHelperCore
import Foundation

struct ClaudeDesktopContext {
    static let bundleIdentifier = "com.anthropic.claudefordesktop"
    static let claudeCodeBundleIdentifier = "com.anthropic.claude-code"

    let appURL: URL
    let application: NSRunningApplication
    let desktopVersion: String
    let bundledClaudeCodeVersion: String

    var axApplication: AXUIElement {
        AXUIElementCreateApplication(application.processIdentifier)
    }

    var status: AppStatus {
        AppStatus(
            bundleIdentifier: Self.bundleIdentifier,
            installed: true,
            running: true,
            accessibilityTrusted: AXIsProcessTrusted(),
            desktopVersion: desktopVersion,
            bundledClaudeCodeVersion: bundledClaudeCodeVersion
        )
    }

    func requireFrontmostForAction() throws {
        guard application.isActive else {
            throw HelperFailure(
                .actionFailed,
                "Claude Desktop must be frontmost before an action. The helper does not activate it automatically."
            )
        }
    }

    static func load(requireAccessibility: Bool = true) throws -> ClaudeDesktopContext {
        let locatedURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleIdentifier)
        let runningApplications = NSRunningApplication.runningApplications(withBundleIdentifier: bundleIdentifier)
        try PreflightGate.validate(
            installed: locatedURL != nil,
            running: !runningApplications.isEmpty,
            accessibilityTrusted: !requireAccessibility || AXIsProcessTrusted()
        )
        guard let appURL = locatedURL,
              let application = runningApplications.first,
              let bundle = Bundle(url: appURL)
        else { fatalError("PreflightGate accepted incomplete application state") }
        guard let desktopVersion = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
              !desktopVersion.isEmpty
        else {
            throw HelperFailure(.versionMetadataMissing, "Claude Desktop has no version in its bundle metadata.")
        }
        let bundledVersion = try latestBundledClaudeCodeVersion()
        return ClaudeDesktopContext(
            appURL: appURL,
            application: application,
            desktopVersion: desktopVersion,
            bundledClaudeCodeVersion: bundledVersion
        )
    }

    private static func latestBundledClaudeCodeVersion() throws -> String {
        let manager = FileManager.default
        guard let support = manager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            throw HelperFailure(.versionMetadataMissing, "The user Application Support directory is unavailable.")
        }
        let root = support.appendingPathComponent("Claude/claude-code", isDirectory: true)
        let directories = (try? manager.contentsOfDirectory(
            at: root,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        )) ?? []

        let candidates: [(SemanticVersion, String)] = directories.compactMap { directory in
            let app = directory.appendingPathComponent("claude.app", isDirectory: true)
            let verified = directory.appendingPathComponent(".verified")
            guard manager.fileExists(atPath: verified.path),
                  let bundle = Bundle(url: app),
                  bundle.bundleIdentifier == claudeCodeBundleIdentifier,
                  let version = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
                  version == directory.lastPathComponent,
                  let semantic = SemanticVersion(version)
            else { return nil }
            return (semantic, version)
        }
        guard let newest = candidates.max(by: { $0.0 < $1.0 }) else {
            throw HelperFailure(
                .versionMetadataMissing,
                "No verified bundled Claude Code bundle metadata is available."
            )
        }
        return newest.1
    }
}
