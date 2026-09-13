import Foundation

/// Relay wire frames the suites feed to `SessionManager.handleRelayMessage`.
///
/// Shared rather than private per suite for the reason `TestAgentBranding`
/// states: two copies of one fixture is how two suites quietly stop testing
/// the same thing. The specific risk here is the key shape — `relaySessionMap`
/// is keyed by `rowID` (`"daemon/id"`), and a suite that seeds that map by
/// hand instead of sending a frame stays green when the ingest path changes
/// the key. Going through `handleRelayMessage` is what keeps a relay fixture
/// honest.
enum RelayFixtures {

    /// A relay Push frame — envelope `source` plus an inner `session_created`,
    /// as the hub forwards it.
    static func push(
        source: String,
        sessionId: String,
        project: String,
        state: String = "working"
    ) -> String {
        frame(source: source, inner: "session_created",
              session: sessionJSON(id: sessionId, project: project, state: state))
    }

    /// A relay Push frame carrying `session_deleted` — how a relay session
    /// really leaves `relaySessionMap`, and therefore how a relay project
    /// group really leaves the rendered payload (#1954).
    ///
    /// `applyRelayInner`'s delete arm removes by `s.rowID`, which is
    /// `"\(daemonID)/\(id)"` — so `source` and `sessionId` must match the
    /// `push(…)` that created the row or the removal is a silent no-op. Read
    /// at `SessionManager+Relay.swift`'s `case "session_deleted"` and
    /// `SessionState.rowID`; the suites that use this assert the row actually
    /// left rather than trusting the frame.
    static func delete(
        source: String,
        sessionId: String,
        project: String,
        state: String = "ready"
    ) -> String {
        frame(source: source, inner: "session_deleted",
              session: sessionJSON(id: sessionId, project: project, state: state))
    }

    /// The envelope both frames share. One builder rather than two literals,
    /// for the reason above: two copies drift, and the `source`/`session_id`
    /// pairing is exactly what a `session_deleted` has to get right.
    private static func frame(source: String, inner: String, session: String) -> String {
        """
        {"type":"push","source":"\(source)","msg":{"type":"\(inner)","session":\(session)}}
        """
    }

    /// The session object the envelope carries. Split from `frame` so neither
    /// builder takes the whole flattened parameter list.
    private static func sessionJSON(id: String, project: String, state: String) -> String {
        """
        {"session_id":"\(id)","state":"\(state)","model":"m",\
        "cwd":"/tmp","project_name":"\(project)","first_seen":0,"updated_at":0}
        """
    }
}
