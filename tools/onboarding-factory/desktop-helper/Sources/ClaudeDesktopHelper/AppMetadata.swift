import AppKit
import ApplicationServices
import DesktopHelperCore
import Foundation

struct BundleMetadata {
    let bundleIdentifier: String?
    let version: String?
}

struct ClaudeCodeFilesystem {
    let applicationSupportDirectory: () -> URL?
    let directories: (URL) throws -> [URL]
    let fileExists: (URL) -> Bool
    let bundleMetadata: (URL) -> BundleMetadata?

    static var live: ClaudeCodeFilesystem {
        let manager = FileManager.default
        return ClaudeCodeFilesystem(
            applicationSupportDirectory: {
                manager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            },
            directories: { root in
                try manager.contentsOfDirectory(
                    at: root,
                    includingPropertiesForKeys: [.isDirectoryKey],
                    options: [.skipsHiddenFiles]
                )
            },
            fileExists: { manager.fileExists(atPath: $0.path) },
            bundleMetadata: { url in
                guard let bundle = Bundle(url: url) else { return nil }
                return BundleMetadata(
                    bundleIdentifier: bundle.bundleIdentifier,
                    version: bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
                )
            }
        )
    }
}

struct DesktopEnvironment {
    let applicationURL: () -> URL?
    let runningApplications: () -> [NSRunningApplication]
    let accessibilityTrusted: () -> Bool
    let desktopVersion: (URL) -> String?
    let bundledClaudeCodeVersion: () throws -> String

    static var live: DesktopEnvironment {
        DesktopEnvironment(
            applicationURL: {
                NSWorkspace.shared.urlForApplication(
                    withBundleIdentifier: ClaudeDesktopContext.bundleIdentifier
                )
            },
            runningApplications: {
                NSRunningApplication.runningApplications(
                    withBundleIdentifier: ClaudeDesktopContext.bundleIdentifier
                )
            },
            accessibilityTrusted: AXIsProcessTrusted,
            desktopVersion: { url in
                Bundle(url: url)?.object(
                    forInfoDictionaryKey: "CFBundleShortVersionString"
                ) as? String
            },
            bundledClaudeCodeVersion: {
                try ClaudeDesktopContext.latestBundledClaudeCodeVersion(filesystem: .live)
            }
        )
    }
}

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

    static func load(
        requireAccessibility: Bool = true,
        environment: DesktopEnvironment = .live
    ) throws -> ClaudeDesktopContext {
        let locatedURL = environment.applicationURL()
        let runningApplications = environment.runningApplications()
        try PreflightGate.validate(
            installed: locatedURL != nil,
            running: !runningApplications.isEmpty,
            accessibilityTrusted: !requireAccessibility || environment.accessibilityTrusted()
        )
        guard let appURL = locatedURL, let application = runningApplications.first else {
            throw HelperFailure(.actionFailed, "Claude Desktop process state changed during preflight.")
        }
        guard let desktopVersion = environment.desktopVersion(appURL),
              !desktopVersion.isEmpty
        else {
            throw HelperFailure(.versionMetadataMissing, "Claude Desktop has no version in its bundle metadata.")
        }
        let bundledVersion = try environment.bundledClaudeCodeVersion()
        return ClaudeDesktopContext(
            appURL: appURL,
            application: application,
            desktopVersion: desktopVersion,
            bundledClaudeCodeVersion: bundledVersion
        )
    }

    static func latestBundledClaudeCodeVersion(
        filesystem: ClaudeCodeFilesystem
    ) throws -> String {
        guard let support = filesystem.applicationSupportDirectory() else {
            throw HelperFailure(.versionMetadataMissing, "The user Application Support directory is unavailable.")
        }
        let root = support.appendingPathComponent("Claude/claude-code", isDirectory: true)
        let directories: [URL]
        do {
            directories = try filesystem.directories(root)
        } catch {
            throw HelperFailure(
                .versionMetadataMissing,
                "The bundled Claude Code directory could not be read."
            )
        }

        let candidates: [(SemanticVersion, String)] = directories.compactMap { directory in
            let app = directory.appendingPathComponent("claude.app", isDirectory: true)
            let verified = directory.appendingPathComponent(".verified")
            guard filesystem.fileExists(verified),
                  let metadata = filesystem.bundleMetadata(app),
                  metadata.bundleIdentifier == claudeCodeBundleIdentifier,
                  let version = metadata.version,
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
