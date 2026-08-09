import SwiftUI

/// Consent wizard for the permission transparency model (issue #570).
///
/// Auto mode appears when a detected agent has unanswered permissions. Its
/// agent set is LOCKED at presentation time (`lockedAgents`, captured by
/// SessionListView): an agent detected mid-decision never injects rows into
/// an open wizard — it gets its own prompt after this one resolves. The
/// locked set also keeps the wizard up if the agent's process exits while
/// the user is deciding; only answers dismiss it (SessionListView's
/// reconcile watches the snapshot), which is equally how the wizard closes
/// live when the web dashboard answers first.
///
/// Review mode (Settings → "Review agent permissions…") shows every agent
/// and every permission with the current grants preloaded, so any decision
/// can be changed later.
struct PermissionWizardView: View {
    enum Mode {
        case auto, review
    }

    let mode: Mode
    /// Agent names the auto wizard presents (ignored in review mode).
    var lockedAgents: [String] = []
    let onClose: () -> Void
    @EnvironmentObject var sessionManager: SessionManager

    /// Toggle drafts keyed "agent/permission". Nothing is sent until Apply
    /// — the explicit click is the consent. Unset keys read as
    /// `defaultValue(for:)`, the single authority for toggle defaults.
    @State private var draft: [String: Bool] = [:]
    /// True while an Apply POST is in flight; guards double submission.
    @State private var submitting = false

    private var agents: [AgentPermissions] {
        let all = sessionManager.permissionsSnapshot?.agents ?? []
        switch mode {
        case .review:
            return all
        case .auto:
            // Locked set, NOT pendingWizardAgents: membership must survive
            // the agent's process exiting mid-decision (detected flips
            // false) and must exclude agents detected after presentation.
            return all.filter { lockedAgents.contains($0.name) }
        }
    }

    /// The permissions shown for one agent: everything in review mode; in
    /// auto mode the unanswered ones plus any whose consent effect failed,
    /// so a broken install is not invisible on the surface the user is
    /// already looking at (#1362). This deliberately does NOT widen
    /// `needsWizard`, so a failure never pops the wizard open by itself.
    private func visiblePermissions(of agent: AgentPermissions) -> [PermissionItem] {
        guard mode == .auto else { return agent.permissions }
        return agent.permissions.filter { $0.state == .pending || $0.effectNotice != nil }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(mode == .auto ? "Agent detected" : "Agent permissions")
                .font(.headline)
                .padding(.horizontal, 20)
                .padding(.top, 20)
                .padding(.bottom, 6)

            Text(mode == .auto
                ? "irrlicht monitors coding agents only with your consent. Choose what it may do for each detected agent."
                : "Everything irrlicht may read or modify, per agent. Toggling a grant off undoes the modification and stops all reading.")
                .font(.caption)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 20)
                .padding(.bottom, 12)

            Divider()
                .padding(.horizontal, 20)

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(agents) { agent in
                        agentSection(agent)
                    }
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 16)
            }
            .frame(maxHeight: 520)
            .fixedSize(horizontal: false, vertical: true)

            Divider()
                .padding(.horizontal, 20)

            HStack {
                Text("Nothing is read or modified until you grant it.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                Spacer()
                if mode == .auto {
                    Button("Decide Later") { onClose() }
                        .disabled(submitting)
                        .tooltip("Keep everything paused; the wizard returns from Settings or when the agent is seen again")
                }
                Button("Apply") { apply() }
                    .disabled(submitting)
                    .keyboardShortcut(.defaultAction)
                    .tooltip("Apply these grants now")
            }
            .padding(.horizontal, 20)
            .padding(.top, 12)
            .padding(.bottom, 20)
        }
        // Matches SettingsView's panel width (issue #940) — review mode swaps
        // this view in as Settings' panel body in the same NSPanel, so a
        // mismatched width would reintroduce the resize jump that issue fixed.
        .frame(width: SessionListView.panelWidth)
        .background(Color(NSColor.windowBackgroundColor))
        .toggleStyle(IrrlichtSwitchToggleStyle())
    }

    @ViewBuilder
    private func agentSection(_ agent: AgentPermissions) -> some View {
        let perms = visiblePermissions(of: agent)
        if !perms.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    Text(agent.displayName)
                        .font(.subheadline)
                        .fontWeight(.medium)
                    if agent.detected {
                        Text("running")
                            .font(.caption2)
                            .foregroundColor(IrrColors.working)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .overlay(
                                RoundedRectangle(cornerRadius: 3)
                                    .stroke(IrrColors.working, lineWidth: 1)
                            )
                            .tooltip("A live \(agent.displayName) process was detected")
                    }
                    Spacer()
                }
                ForEach(perms) { perm in
                    permissionRow(agent: agent, perm: perm)
                }
            }
        }
    }

    private func permissionRow(agent: AgentPermissions, perm: PermissionItem) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            LeadingToggle(
                isOn: draftBinding(agent: agent, perm: perm),
                label: perm.title,
                info: "\(perm.touches). \(perm.detail)"
            )
            Text(perm.featureUnlocked)
                .font(.caption)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.leading, 38)
            if let notice = perm.effectNotice {
                effectNoticeRow(agent: agent, perm: perm, notice: notice)
            }
        }
    }

    /// "Granted, but not applied" — the consent stands and the effect did
    /// not land (#1362). Red in-content alert chrome, matching
    /// SessionRowView's inline warning strip.
    private func effectNoticeRow(agent: AgentPermissions, perm: PermissionItem,
                                 notice: EffectNotice) -> some View {
        HStack(alignment: .top, spacing: 6) {
            Image(systemName: "exclamationmark.triangle")
                .font(.caption)
                .foregroundColor(IrrColors.pressureHigh)
            VStack(alignment: .leading, spacing: 1) {
                Text(notice.label)
                    .font(.caption)
                    .fontWeight(.medium)
                    .foregroundColor(IrrColors.pressureHigh)
                Text(notice.reason)
                    .font(.caption2)
                    .foregroundColor(IrrColors.pressureHigh)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 6)
            Button(notice.retryLabel) { retry(agent: agent, perm: perm, notice: notice) }
                .disabled(submitting)
                .tooltip("Run this permission's effect again without changing your decision")
        }
        .padding(6)
        .background(IrrColors.pressureHigh.opacity(0.08))
        .cornerRadius(IrrRadius.sm)
        .padding(.leading, 38)
        .padding(.top, 4)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(perm.title): \(notice.label). \(notice.reason)")
    }

    /// Re-submits the SAME decision for one permission. The daemon re-runs
    /// the closure because it has a recorded failure for it, so the user
    /// never has to revoke and re-grant (#1362).
    private func retry(agent: AgentPermissions, perm: PermissionItem, notice: EffectNotice) {
        submitting = true
        let manager = sessionManager
        let answer = PermissionAnswer(agent: agent.name, permission: perm.key, grant: notice.grant)
        Task {
            _ = await manager.answerPermissions([answer])
            submitting = false
        }
    }

    private func draftKey(_ agent: AgentPermissions, _ perm: PermissionItem) -> String {
        "\(agent.name)/\(perm.key)"
    }

    private func draftBinding(agent: AgentPermissions, perm: PermissionItem) -> Binding<Bool> {
        let key = draftKey(agent, perm)
        return Binding(
            get: { draft[key] ?? defaultValue(for: perm) },
            set: { draft[key] = $0 }
        )
    }

    /// Pending items default on (granting is the value proposition; the
    /// explicit Apply click is the consent). Answered items show their
    /// current state.
    private func defaultValue(for perm: PermissionItem) -> Bool {
        perm.state == .pending ? true : perm.state == .granted
    }

    /// Builds the answer batch: in auto mode every displayed pending item
    /// is answered explicitly (off = deny); in review mode only changes
    /// against the current state are submitted.
    ///
    /// Auto mode does NOT close on Apply — the daemon's response snapshot
    /// resolves the locked agents' pending items and SessionListView's
    /// reconcile dismisses the wizard. A failed POST therefore keeps the
    /// wizard up for a retry instead of silently dropping the consent
    /// decisions while monitoring stays paused.
    private func apply() {
        var answers: [PermissionAnswer] = []
        for agent in agents {
            for perm in visiblePermissions(of: agent) {
                let grant = draft[draftKey(agent, perm)] ?? defaultValue(for: perm)
                if perm.shouldSubmit(grant: grant) {
                    answers.append(PermissionAnswer(agent: agent.name, permission: perm.key, grant: grant))
                }
            }
        }
        guard !answers.isEmpty else {
            onClose()
            return
        }
        submitting = true
        let manager = sessionManager
        let isReview = mode == .review
        let answered = Set(answers.map(\.agent))
        Task {
            let ok = await manager.answerPermissions(answers)
            submitting = false
            // The daemon answers 200 whether or not the consent effect
            // succeeded, so `ok` alone is not "it worked". Closing here on
            // a failed Apply would hide the warning the review wizard is
            // the main place to see (#1362).
            let failed = (manager.permissionsSnapshot?.agents ?? [])
                .filter { answered.contains($0.name) }
                .contains { $0.hasFailedEffect }
            if isReview && ok && !failed {
                onClose()
            }
        }
    }
}
