import Foundation

/// Consent state for the permission transparency wizard (issue #570),
/// mirroring GET /api/v1/permissions. The daemon is the single source of
/// truth: nothing is read or modified on the user's system until the
/// corresponding permission is granted.
struct PermissionsSnapshot: Decodable, Equatable {
    /// "ask" (production) or "grant-all" (demo/record/test daemons —
    /// wizard suppressed).
    let mode: String
    let agents: [AgentPermissions]
}

struct AgentPermissions: Decodable, Equatable, Identifiable {
    let name: String
    let displayName: String
    /// True while the daemon's detection poller sees a live process for
    /// this agent. Detection is the consent-free baseline — it never reads
    /// files or creates sessions.
    let detected: Bool
    let permissions: [PermissionItem]

    var id: String { name }

    /// True when the auto wizard should include this agent: it is running
    /// and has at least one unanswered permission.
    var needsWizard: Bool {
        detected && permissions.contains { $0.state == .pending }
    }

    enum CodingKeys: String, CodingKey {
        case name
        case displayName = "display_name"
        case detected
        case permissions
    }
}

struct PermissionItem: Decodable, Equatable, Identifiable {
    enum State: String, Decodable {
        case pending, granted, denied
    }

    let key: String
    /// "modify" (writes a file outside the daemon home) or "observe"
    /// (read-only monitoring).
    let kind: String
    let state: State
    let title: String
    let featureUnlocked: String
    let touches: String
    let detail: String

    var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, kind, state, title, touches, detail
        case featureUnlocked = "feature_unlocked"
    }
}

/// One decision in the POST /api/v1/permissions/answer body.
struct PermissionAnswer: Encodable {
    let agent: String
    let permission: String
    let grant: Bool
}
