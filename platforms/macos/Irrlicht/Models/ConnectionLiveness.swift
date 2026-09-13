import Foundation

/// Decides whether a WebSocket link has gone silent (#1953).
///
/// Pure, static, and free of `SessionManager` so the decision is unit-testable
/// without a live socket — the same shape `DaemonHealth.faults` uses for the
/// same reason.
///
/// WHY A CLOCK AND NOT JUST A PING. A Mac waking from standby can leave a
/// WebSocket half-open: the peer is gone, but no FIN and no error ever arrive,
/// so `URLSessionWebSocketTask.receive()` waits forever. Read at `a680d5ea`,
/// not assumed: both reconnect loops — `SessionManager+WebSocket.swift`'s
/// `connect()` and `SessionManager+Relay.swift`'s `relayConnect()` — reach
/// their backoff `schedule…Connect(after:)` call only *after* `receive()`
/// throws, so a `receive()` that never returns is a reconnect that never
/// happens. A ping alone does not close that hole either: the pong callback on
/// a half-open socket may simply never fire, which is silence of exactly the
/// same kind. So liveness is decided by a wall clock, and the ping only feeds
/// it fresh evidence.
enum ConnectionLiveness {
    /// Whether a link that last delivered a frame at `lastFrameAt` has now been
    /// silent for longer than `deadline`.
    ///
    /// Three edges, each pinned by a case in `ConnectionLivenessTests` and by a
    /// named mutant in that file's mutant table:
    ///
    /// - A `nil` `lastFrameAt` is **not** stale, because there is no elapsed
    ///   time to measure — not because such a link is healthy. That distinction
    ///   is load-bearing: a socket that completes its handshake and then goes
    ///   silent has a nil stamp too, and treating nil as permanently fine would
    ///   make it immortal. `SessionManager`'s tick therefore *starts a clock*
    ///   for any live socket that has none, and only this predicate's caller
    ///   ever sees nil for more than one tick — a link with no socket at all.
    ///   Reaping also resets the stamp to nil, so one reap cannot cascade into
    ///   killing its own replacement socket before that socket's first tick.
    /// - Exactly at the deadline is **not** stale: the comparison is strict, so
    ///   `deadline` reads as the longest tolerated silence rather than the
    ///   shortest fatal one.
    /// - A `lastFrameAt` in the future — a wall clock corrected backwards over
    ///   the stamp, which is precisely what a wake can do — gives a negative
    ///   elapsed time and is **not** stale, so a clock jump cannot by itself
    ///   recycle a healthy link.
    static func isStale(lastFrameAt: Date?, now: Date, deadline: TimeInterval) -> Bool {
        guard let lastFrameAt else { return false }
        return now.timeIntervalSince(lastFrameAt) > deadline
    }
}
