import SwiftUI

/// The Gas Town orchestrator panel — global agents (mayor/deacon/boot) and
/// convoy progress — rendered under the ⛽ Gas Town group header. Mirrors the
/// web overlay (`platforms/web/irrlicht.js` renderOrchestrator). Hides itself
/// when there is no live orchestrator snapshot.
struct GasTownOrchestratorView: View {
    @EnvironmentObject var sessionManager: SessionManager

    var body: some View {
        if let st = sessionManager.orchestratorState, st.running {
            VStack(alignment: .leading, spacing: 3) {
                if let agents = st.globalAgents, !agents.isEmpty {
                    ForEach(agents) { ga in
                        globalAgentRow(ga)
                    }
                }

                let convoys = st.convoys
                if !convoys.isEmpty {
                    Text("\u{1F69A} Convoys")
                        .font(.system(size: 9, weight: .semibold, design: .monospaced))
                        .foregroundColor(.secondary)
                        .padding(.top, 2)
                    ForEach(convoys) { c in
                        convoyRow(c)
                    }
                }
            }
            .padding(.leading, 16)
            .padding(.vertical, 2)
        }
    }

    private func globalAgentRow(_ ga: OrchestratorState.GlobalAgent) -> some View {
        HStack(spacing: 5) {
            Text((ga.icon.map { $0 + " " } ?? "") + ga.role)
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(.primary)
            Circle()
                .fill(IrrColors.forState(ga.state))
                .frame(width: 6, height: 6)
            Text(ga.sessionID.map { String($0.prefix(6)) } ?? "idle")
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(.secondary)
            Spacer()
        }
        .tooltip(ga.description ?? ga.role)
    }

    private func convoyRow(_ c: OrchestratorState.WorkUnit) -> some View {
        HStack(spacing: 5) {
            Text(c.name)
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(c.isDone ? .secondary : .primary)
                .strikethrough(c.isDone)
                .lineLimit(1)
                .truncationMode(.tail)
            Text(dotBar(total: c.total, done: c.done))
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(c.isDone ? IrrColors.ready : IrrColors.working)
            Text("\(c.done) / \(c.total)")
                .font(.system(size: 9, design: .monospaced))
                .foregroundColor(.secondary)
            if c.isDone {
                Text("\u{2713}").font(.system(size: 9)).foregroundColor(IrrColors.ready)
            }
            Spacer()
        }
    }

    /// Progress dots, ported from the web `dotBar` (max 7 dots; scaled when the
    /// total exceeds the cap). Filled ● / empty ○.
    private func dotBar(total: Int, done: Int) -> String {
        let maxDots = 7
        let t = max(0, total)
        let d = max(0, done)
        let totalDots = min(t, maxDots)
        if totalDots <= 0 { return "" }
        let filled = t <= maxDots ? d : Int((Double(d) / Double(t) * Double(maxDots)).rounded())
        let filledDots = max(0, min(filled, totalDots))
        let emptyDots = max(0, min(totalDots - filledDots, maxDots))
        return String(repeating: "\u{25CF}", count: filledDots) +
               String(repeating: "\u{25CB}", count: emptyDots)
    }
}
