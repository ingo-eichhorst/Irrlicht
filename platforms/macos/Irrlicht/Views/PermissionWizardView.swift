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

    /// The permissions shown for one agent: pending only in auto mode,
    /// everything in review mode.
    private func visiblePermissions(of agent: AgentPermissions) -> [PermissionItem] {
        mode == .auto ? agent.permissions.filter { $0.state == .pending } : agent.permissions
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
        .frame(width: 360)
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
                switch perm.state {
                case .pending:
                    answers.append(PermissionAnswer(agent: agent.name, permission: perm.key, grant: grant))
                case .granted, .denied:
                    if grant != (perm.state == .granted) {
                        answers.append(PermissionAnswer(agent: agent.name, permission: perm.key, grant: grant))
                    }
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
        Task {
            let ok = await manager.answerPermissions(answers)
            submitting = false
            if isReview && ok {
                onClose()
            }
        }
    }
}
