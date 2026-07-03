import AppKit
import SwiftUI

// MARK: - Recursive Group View (renders one API group with optional sub-groups)

struct GroupView: View {
    let group: SessionManager.AgentGroup
    var depth: Int = 0
    @EnvironmentObject var sessionManager: SessionManager
    @AppStorage("projectCostTimeframe") private var costTimeframeRaw: String = CostTimeframe.day.rawValue
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false

    // Source-of-truth for expansion lives on SessionManager so
    // SessionListView's height estimator can see it too.
    private var isExpanded: Bool {
        !sessionManager.collapsedGroupNames.contains(group.name)
    }

    private func toggleExpansion() {
        if sessionManager.collapsedGroupNames.contains(group.name) {
            sessionManager.collapsedGroupNames.remove(group.name)
        } else {
            sessionManager.collapsedGroupNames.insert(group.name)
        }
    }

    private var costTimeframe: CostTimeframe { CostTimeframe.from(costTimeframeRaw) }

    private var agentCount: Int {
        let direct = group.agents?.count ?? 0
        let nested = (group.groups ?? []).reduce(0) { $0 + ($1.agents?.count ?? 0) }
        return direct + nested
    }

    private var isTopLevel: Bool { depth == 0 }

    private var groupExpandTooltip: String {
        let action = isExpanded ? "Collapse" : "Expand"
        if isTopLevel && group.isGasTown {
            return "\(action) Gas Town group (external API calls)"
        }
        return "\(action) group"
    }

    /// Formatted cost for this group in the currently-selected time frame.
    /// Returns nil only when there is no cost data at all (hides the toggle).
    /// Returns "$0 / <frame>" when data exists for other frames but not this one,
    /// so the toggle remains visible and clickable.
    private var formattedCost: String? {
        guard showCostDisplay, isTopLevel, !group.isGasTown else { return nil }
        guard let costs = group.costs, !costs.isEmpty else { return nil }
        let v = costs[costTimeframe.rawValue] ?? 0
        guard v > 0 else { return "$0" + costTimeframe.suffix }
        let formatted: String
        if v < 0.01 { formatted = "<$0.01" }
        else if v >= 100 { formatted = String(format: "$%.0f", v) }
        else { formatted = String(format: "$%.2f", v) }
        return formatted + costTimeframe.suffix
    }

    private func cycleCostTimeframe() {
        costTimeframeRaw = costTimeframe.next().rawValue
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Button(action: {
                    // Instant toggle — any withAnimation here fights the
                    // popover's own resize timing and produces visible
                    // flicker on both expand and collapse.
                    toggleExpansion()
                }) {
                    HStack(spacing: 6) {
                        Image(systemName: isExpanded ? "chevron.down" : "chevron.right")
                            .font(.system(size: 8))
                            .foregroundColor(.secondary)
                            .frame(width: 10)

                        if isTopLevel && group.isGasTown {
                            Text("\u{26FD}")
                                .font(.system(size: 10))
                        }

                        Text(group.name)
                            .font(.system(isTopLevel ? .caption : .caption2, design: .monospaced))
                            .fontWeight(isTopLevel ? .semibold : .medium)
                            .foregroundColor(isTopLevel ? .primary : .secondary)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .tooltip(groupExpandTooltip)

                if let cost = formattedCost {
                    Button(action: cycleCostTimeframe) {
                        Text(cost)
                            .font(.system(size: 9, weight: .medium, design: .monospaced))
                            .foregroundColor(.secondary)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .tooltip("Click to cycle time frame (day → week → month → year)")
                }

                // Rig/codebase status (Codebase.Status) on nested rig headers.
                if !isTopLevel, let status = group.status, !status.isEmpty {
                    Text(status.uppercased())
                        .font(.system(size: 9, weight: .semibold, design: .monospaced))
                        .foregroundColor(IrrColors.forState(status))
                        .tooltip("Rig status: \(status)")
                }

                Spacer()

                let count = isTopLevel ? agentCount : (group.agents?.count ?? 0)
                Text(isTopLevel ? "\(count) \(count == 1 ? "session" : "sessions")" : "\(count)")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(isTopLevel ? 0.7 : 0.5))

                if isTopLevel, sessionManager.apiGroups.count > 1,
                   let idx = sessionManager.apiGroups.firstIndex(where: { $0.name == group.name }) {
                    HStack(spacing: 0) {
                        reorderButton(icon: "chevron.up", tooltip: "Move group up", disabled: idx == 0) {
                            sessionManager.moveProjectGroupUp(name: group.name)
                        }
                        reorderButton(icon: "chevron.down", tooltip: "Move group down", disabled: idx == sessionManager.apiGroups.count - 1) {
                            sessionManager.moveProjectGroupDown(name: group.name)
                        }
                    }
                }
            }
            .padding(.horizontal, isTopLevel ? 12 : 20)
            .padding(.vertical, isTopLevel ? 4 : 3)

            if isExpanded {
                ForEach(Array((group.agents ?? []).enumerated()), id: \.element.rowID) { index, session in
                    SessionRowView(
                        session: session,
                        agentNumber: index + 1,
                        activeSubagentCount: session.activeSubagentCount
                    )
                    .padding(.leading, isTopLevel ? 8 : 16)
                }

                ForEach(group.groups ?? [], id: \.name) { subGroup in
                    GroupView(group: subGroup, depth: depth + 1)
                }
            }
        }
    }

    private func reorderButton(icon: String, tooltip: String, disabled: Bool, action: @escaping () -> Void) -> some View {
        Button {
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) { action() }
        } label: {
            Image(systemName: icon)
                .font(.system(size: 10))
                .foregroundColor(.secondary)
                .frame(width: 14, height: 20)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.3 : 1.0)
        .tooltip(tooltip)
    }
}
