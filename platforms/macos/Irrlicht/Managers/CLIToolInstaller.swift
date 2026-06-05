import Foundation

/// Installs the bundle-embedded `irrlicht-ls` CLI onto the user's PATH via a
/// symlink. Consent-first (#608): nothing happens at launch — the PKG
/// installer and `install.sh` link automatically, and DMG drag-install users
/// click "Install Command-Line Tool" in Settings, which lands here.
enum CLIToolInstaller {
    /// Candidate bin directories, in preference order. `/usr/local/bin` is on
    /// the default PATH but usually root-owned; `/opt/homebrew/bin` is
    /// user-writable on Apple Silicon Homebrew setups.
    static let defaultCandidates = ["/usr/local/bin", "/opt/homebrew/bin"]

    enum Status: Equatable {
        case unavailable          // binary not in this bundle (dev builds)
        case notInstalled
        case installed(path: String)
    }

    enum InstallResult: Equatable {
        case installed(path: String)
        case failed(message: String)
    }

    /// The embedded CLI binary — same aux-executable pattern as
    /// DaemonManager.bundledDaemonURL. Nil in SwiftPM dev bundles, which
    /// don't embed the Go binaries.
    static var bundledLs: URL? {
        guard let url = Bundle.main.url(forAuxiliaryExecutable: "irrlicht-ls"),
              FileManager.default.isExecutableFile(atPath: url.path) else {
            return nil
        }
        return url
    }

    static func status(candidates: [String] = defaultCandidates) -> Status {
        guard let source = bundledLs else { return .unavailable }
        let fm = FileManager.default
        for dir in candidates {
            let link = dir + "/irrlicht-ls"
            if let dest = try? fm.destinationOfSymbolicLink(atPath: link),
               URL(fileURLWithPath: dest).standardizedFileURL == source.standardizedFileURL {
                return .installed(path: link)
            }
        }
        return .notInstalled
    }

    /// First writable candidate directory, or nil when none qualifies.
    /// Pure given the injected writability check — the unit-test seam.
    static func chooseTarget(
        candidates: [String],
        isWritableDir: (String) -> Bool = { dir in
            var isDir: ObjCBool = false
            return FileManager.default.fileExists(atPath: dir, isDirectory: &isDir)
                && isDir.boolValue
                && FileManager.default.isWritableFile(atPath: dir)
        }
    ) -> String? {
        candidates.first(where: isWritableDir)
    }

    /// Create (or refresh) the symlink in the first writable candidate.
    /// Refuses to replace a regular file — only stale symlinks are replaced.
    static func install(candidates: [String] = defaultCandidates) -> InstallResult {
        guard let source = bundledLs else {
            return .failed(message: "irrlicht-ls is not embedded in this build")
        }
        guard let dir = chooseTarget(candidates: candidates) else {
            return .failed(message: "No writable bin directory found. Run: sudo ln -sf \"\(source.path)\" /usr/local/bin/irrlicht-ls")
        }
        let link = dir + "/irrlicht-ls"
        let fm = FileManager.default
        if fm.fileExists(atPath: link) {
            guard (try? fm.destinationOfSymbolicLink(atPath: link)) != nil else {
                return .failed(message: "\(link) exists and is not a symlink — refusing to replace it")
            }
            try? fm.removeItem(atPath: link)
        }
        do {
            try fm.createSymbolicLink(atPath: link, withDestinationPath: source.path)
            return .installed(path: link)
        } catch {
            return .failed(message: "Could not create \(link): \(error.localizedDescription)")
        }
    }
}
