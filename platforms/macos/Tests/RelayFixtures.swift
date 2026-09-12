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
        """
        {"type":"push","source":"\(source)","msg":{"type":"session_created",\
        "session":{"session_id":"\(sessionId)","state":"\(state)","model":"m",\
        "cwd":"/tmp","project_name":"\(project)","first_seen":0,"updated_at":0}}}
        """
    }
}
