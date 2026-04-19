import Foundation

/// Brings a specific window or tab of a given terminal/IDE to the
/// foreground. One conforming type per supported `$TERM_PROGRAM`.
///
/// Activators are consulted by `SessionLauncher.jump(_:)` via the registry
/// — see `SessionLauncher.swift`. Register a new host by adding an instance
/// to that registry; no other dispatch code needs to change.
///
/// The contract is "works or it doesn't, no fallback": if `activate` can
/// precisely target the session's window, it does; otherwise it returns
/// false and the caller logs but does not cascade to another activator.
/// This avoids misleading partial-behaviour (e.g. landing in Finder when
/// the user asked for a worktree's editor window).
protocol HostActivator {
    /// The `$TERM_PROGRAM` value this activator handles (e.g. `"iTerm.app"`,
    /// `"Apple_Terminal"`, `"vscode"`). Used as the registry dispatch key.
    var termProgram: String { get }

    /// macOS bundle identifier of the target app. Exposed so callers that
    /// want a pure app-level activation can reach it without knowing the
    /// concrete activator type.
    var bundleID: String { get }

    /// Perform window/tab activation for `session`. Returns true when the
    /// activator attempted a real target (regardless of whether macOS then
    /// animated the focus); false when the activator lacks the signals
    /// needed for precise targeting or a required permission was denied.
    func activate(_ session: SessionState) -> Bool
}
