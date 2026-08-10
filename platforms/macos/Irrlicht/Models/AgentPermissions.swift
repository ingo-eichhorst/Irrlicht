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

    /// Permissions the user GRANTED whose effect is not in force (#1385).
    ///
    /// Computed by the daemon, not here: the web dashboard reads the same
    /// list, and the two surfaces must never disagree about the count. The
    /// daemon omits the key entirely when the list is empty, so this
    /// defaults to `[]` rather than being optional — an absent key means
    /// "nothing wrong", not "unknown".
    let unappliedGrants: [UnappliedGrant]

    /// The passive indicator's content, or nil when there is nothing to
    /// say. Pure — unit-tested without rendering the view.
    var unappliedGrantSummary: UnappliedGrantSummary? {
        UnappliedGrantSummary(items: unappliedGrants)
    }

    enum CodingKeys: String, CodingKey {
        case mode
        case agents
        case unappliedGrants = "unapplied_grants"
    }

    // Written out rather than synthesized: synthesized decoding requires
    // every non-optional key to be present, and `unapplied_grants` is
    // omitempty on the wire.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        mode = try c.decode(String.self, forKey: .mode)
        agents = try c.decodeIfPresent([AgentPermissions].self, forKey: .agents) ?? []
        unappliedGrants = try c.decodeIfPresent([UnappliedGrant].self, forKey: .unappliedGrants) ?? []
    }

    /// Memberwise init for tests and previews; the custom `init(from:)`
    /// above suppresses the synthesized one.
    init(mode: String, agents: [AgentPermissions], unappliedGrants: [UnappliedGrant] = []) {
        self.mode = mode
        self.agents = agents
        self.unappliedGrants = unappliedGrants
    }
}

/// One permission the user granted whose consent effect is not in force
/// (#1385): the consent stands, the modification is absent.
///
/// `reason` is the daemon's effect error verbatim, and it is what keeps the
/// diagnoses apart — an install that FAILED (#1362) and a refusal because
/// the CLI is below its declared version floor (#1365) both arrive here,
/// and the refusal's reason carries its own advice ("upgrade and grant
/// again"). Replacing it with a classification code would be the collapse
/// the aggregate exists to avoid.
struct UnappliedGrant: Decodable, Equatable, Identifiable {
    let agent: String
    let agentDisplayName: String
    let key: String
    let title: String
    let reason: String

    var id: String { agent + "/" + key }

    enum CodingKeys: String, CodingKey {
        case agent
        case agentDisplayName = "agent_display_name"
        case key
        case title
        case reason
    }
}

/// The rendered form of the aggregate. Mirrors the web's
/// `unappliedGrantSummary` word for word so the two surfaces read
/// identically.
struct UnappliedGrantSummary: Equatable {
    let count: Int
    let text: String
    let items: [UnappliedGrant]

    init?(items: [UnappliedGrant]) {
        guard !items.isEmpty else { return nil }
        self.items = items
        count = items.count
        text = count == 1
            ? "1 permission is granted but not applied"
            : "\(count) permissions are granted but not applied"
    }
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
    ///
    /// Deliberately NOT widened to cover failed effects (#1362): a
    /// persistent install failure would otherwise re-present the wizard
    /// forever. Failures keep an OPEN wizard open
    /// (`hasUnresolvedPermissions`) and are always visible in review mode.
    var needsWizard: Bool {
        detected && hasPending
    }

    /// True when any of this agent's permissions is still unanswered.
    var hasPending: Bool {
        permissions.contains { $0.state == .pending }
    }

    /// True while this agent still needs the wizard's attention: an
    /// unanswered permission, or an answered one whose consent effect
    /// failed. Drives auto-wizard DISMISSAL — without the second arm, a
    /// grant whose Apply failed reads as "answered", the wizard closes,
    /// and the user never sees why nothing works (#1362).
    var hasUnresolvedPermissions: Bool {
        hasPending || hasFailedEffect
    }

    /// True when any of this agent's permissions has a failed consent
    /// effect. Distinct from `hasUnresolvedPermissions`: the review wizard
    /// closes on Apply and must NOT close on a 200 that installed nothing
    /// — the daemon answers 200 whether or not the closure succeeded.
    var hasFailedEffect: Bool {
        permissions.contains { $0.effectNotice != nil }
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
    /// Why the consent effect for the CURRENT state failed, absent when it
    /// succeeded (issue #1362). `granted` and `applied` are two different
    /// facts: a granted permission carrying this means the user said yes
    /// and the modification did NOT happen — the daemon never rolls the
    /// grant back over a failed effect.
    let effectError: String?

    var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, kind, state, title, touches, detail
        case featureUnlocked = "feature_unlocked"
        case effectError = "effect_error"
    }

    /// Whether this permission belongs in the wizard's Apply batch, given
    /// the toggle's current position. Unanswered items are always
    /// submitted; answered ones only when the decision changed — OR when
    /// the consent effect failed, in which case the identical answer is a
    /// retry the daemon acts on (#1362). Pure; unit-tested.
    func shouldSubmit(grant: Bool) -> Bool {
        switch state {
        case .pending:
            return true
        case .granted, .denied:
            if grant != (state == .granted) { return true }
            return effectNotice != nil
        }
    }

    /// The warning to render for a permission whose consent effect failed,
    /// or nil when there is nothing wrong. Pure — unit-tested without
    /// rendering the view.
    var effectNotice: EffectNotice? {
        guard let reason = effectError, !reason.isEmpty else { return nil }
        switch state {
        case .granted:
            return EffectNotice(label: "Granted, but not applied", reason: reason,
                                retryLabel: "Retry", grant: true)
        case .denied:
            return EffectNotice(label: "Revoked, but not undone", reason: reason,
                                retryLabel: "Retry undo", grant: false)
        case .pending:
            // Pending never runs an effect; ignore a stray error.
            return nil
        }
    }
}

/// The rendered form of a failed consent effect (issue #1362).
struct EffectNotice: Equatable {
    let label: String
    let reason: String
    let retryLabel: String
    /// The decision to re-submit on Retry — always the CURRENT one, so a
    /// retry can never silently flip the user's consent.
    let grant: Bool
}

/// One decision in the POST /api/v1/permissions/answer body.
struct PermissionAnswer: Encodable {
    let agent: String
    let permission: String
    let grant: Bool
}
