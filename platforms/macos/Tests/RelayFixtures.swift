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

    /// A RAW daemon frame: the bare `WsEnvelope`, with no relay envelope around
    /// it and therefore no `source`.
    ///
    /// `handleRelayMessage`'s `default` arm calls
    /// `applyRelayInner(inner, daemonID: nil)` for it — "the URL pointed at a
    /// daemon, handle it like one" — so it is the ingest that produces a
    /// session with `daemonID == nil`, which is what the menu bar's `location`
    /// bucketing calls `Local` (#1955 phase 2). Read at
    /// `SessionManager+Relay.swift`'s `default:` case and `applyRelayInner`'s
    /// `s.daemonID = daemonID`; a test that wants an un-daemoned session in
    /// `sessions` gets one from here rather than by assigning `sessionMap`,
    /// for the reason at the top of this file.
    static func localFrame(
        sessionId: String,
        project: String,
        state: String = "working"
    ) -> String {
        """
        {"type":"session_created","session":\(sessionJSON(id: sessionId, project: project, state: state))}
        """
    }

    /// A relay `snapshot` control frame announcing connected daemons and the
    /// labels the hub knows them by — the frame that populates
    /// `SessionManager.relayDaemons`, and therefore the only honest way to give
    /// a test a daemon with a NAME rather than a bare id.
    ///
    /// Lifted here from `SessionManagerApiGroupsTests`' private `relaySnapshot`
    /// for the reason this file exists: #1955 phase 2's per-daemon menu bar
    /// buckets are named from that map, so two suites now depend on the frame
    /// shape and a private copy is how they would stop agreeing about it.
    /// Verified against the decoder rather than the old copy —
    /// `SessionManager+Relay.swift`'s `RelayDaemonInfo` keys on `daemon_id`,
    /// `daemon_label` and `status`, and the `"snapshot"` arm at `:345-354`
    /// reads `daemons`.
    static func snapshot(daemons: [(id: String, label: String)]) -> String {
        let entries = daemons.map {
            """
            {"daemon_id":"\($0.id)","daemon_label":"\($0.label)","status":"connected"}
            """
        }
        return """
        {"type":"snapshot","daemons":[\(entries.joined(separator: ","))]}
        """
    }

    /// A relay `daemon_status` control frame — one daemon connecting or
    /// disconnecting. `status: "disconnected"` is what moves a daemon from
    /// `relayDaemons` into `offlineDaemons` (fade, don't delete — #540), which
    /// is the state where the two label maps disagree.
    ///
    /// `label` is OPTIONAL and the key is omitted when it is nil, rather than
    /// defaulting to `""`. An empty string decodes as `Optional("")`, and the
    /// connect arm's `relayDaemons[id] = ctrl.daemonLabel ?? id` would then
    /// store an empty label instead of falling back to the id — so a frame
    /// meant to say "no label" would name a menu bar bucket `""`. Only
    /// disconnect callers exist today, which is exactly why the trap would sit
    /// unnoticed until the first connect caller.
    static func daemonStatus(
        daemonID: String, status: String, label: String? = nil
    ) -> String {
        let labelField = label.map { ",\"daemon_label\":\"\($0)\"" } ?? ""
        return """
        {"type":"daemon_status","daemon_id":"\(daemonID)"\(labelField),"status":"\(status)"}
        """
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
