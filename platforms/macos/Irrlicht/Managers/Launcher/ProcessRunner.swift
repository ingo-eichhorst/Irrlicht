import Foundation
import OSLog

/// Runs a child process synchronously with a timeout. Used by activators that
/// need to exec a CLI (e.g. `kitten @`, `tmux`) rather than AppleScript or AX.
enum ProcessRunner {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "ProcessRunner")

    struct Result {
        let status: Int32
        let stdout: String
        let stderr: String
    }

    /// Launches `launchPath` with `args` and blocks until the process exits or
    /// `timeout` expires. On timeout the process is terminated and `status` is
    /// set to -1.
    @discardableResult
    static func run(_ launchPath: String, args: [String], timeout: TimeInterval = 3.0) -> Result {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: launchPath)
        proc.arguments = args

        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        proc.standardOutput = stdoutPipe
        proc.standardError = stderrPipe

        do {
            try proc.run()
        } catch {
            logger.error("ProcessRunner failed to launch \(launchPath, privacy: .public): \(error.localizedDescription, privacy: .public)")
            return Result(status: -1, stdout: "", stderr: error.localizedDescription)
        }

        let deadline = Date(timeIntervalSinceNow: timeout)
        while proc.isRunning && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.02)
        }
        if proc.isRunning {
            proc.terminate()
            logger.info("ProcessRunner timed out after \(timeout)s: \(launchPath, privacy: .public)")
            return Result(status: -1, stdout: "", stderr: "timeout")
        }

        let outData = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
        let errData = stderrPipe.fileHandleForReading.readDataToEndOfFile()
        return Result(
            status: proc.terminationStatus,
            stdout: String(decoding: outData, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines),
            stderr: String(decoding: errData, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        )
    }
}
