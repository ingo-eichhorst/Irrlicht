import AppKit
import SwiftUI

// MARK: - Session Row View

/// Shared shape for the row's single-line notice pills (pending question,
/// cache-bloat badge): tinted text on a dim background, full-width,
/// truncating rather than wrapping.
///
/// `color` and `wash` are separate (issue #984): `wash` (defaulting to
/// `color`) tints the 12%-alpha background, kept at the plain brand hue so
/// dots/glows elsewhere stay visually consistent; `color` draws the text and
/// can be a different, per-appearance-tuned value where the brand hue itself
/// doesn't clear WCAG AA against that wash (see `IrrColors.waitingPillText`).
///
/// `lineLimit` defaults to 1 (the cache-bloat badge); the question pill
/// requests more (issue #979) since the daemon no longer pre-truncates it to
/// a single-line-sized cut.
private struct PillText: ViewModifier {
    let color: Color
    let wash: Color
    var font: Font = .system(size: 10)
    var lineLimit: Int = 1

    func body(content: Content) -> some View {
        content
            .font(font)
            .foregroundColor(color)
            .lineLimit(lineLimit)
            .truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 5)
            .padding(.vertical, 3)
            .background(wash.opacity(0.12))
            .cornerRadius(IrrRadius.sm)
    }
}

extension View {
    fileprivate func pill(color: Color, wash: Color? = nil, font: Font = .system(size: 10), lineLimit: Int = 1) -> some View {
        modifier(PillText(color: color, wash: wash ?? color, font: font, lineLimit: lineLimit))
    }
}

struct ContextBar: View {
    let utilization: Double
    let pressureColor: Color
    var label: String? = nil

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: IrrRadius.xs)
                    .fill(IrrColors.trackFill)
                RoundedRectangle(cornerRadius: IrrRadius.xs)
                    .fill(pressureColor)
                    .frame(width: geo.size.width * min(CGFloat(utilization) / 100, 1.0))
                if let label {
                    Text(label)
                        .font(.system(size: 8, weight: .medium, design: .monospaced))
                        .foregroundColor(.secondary.opacity(0.8))
                        .padding(.trailing, 4)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
        }
    }
}

struct SessionRowView: View {
    let session: SessionState
    let agentNumber: Int
    var activeSubagentCount: Int = 0
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    // costDisplayMode selects what the cost slot shows — "cost" ($) or "co2"
    // (estimated CO2e), issue #829. App-wide via @AppStorage so clicking any
    // row's figure cycles all rows together, mirroring the web dashboard's
    // localStorage-backed costDisplayMode.
    @AppStorage("costDisplayMode") private var costDisplayModeRaw: String = "cost"
    @AppStorage("displayMode") private var displayModeRaw: String = DisplayMode.context.rawValue
    @AppStorage(ContextPressureThreshold.valueKey) private var contextThresholdValue: Double = ContextPressureThreshold.defaultValue
    @AppStorage(ContextPressureThreshold.unitKey) private var contextThresholdUnitRaw: String = ContextPressureThreshold.defaultUnit.rawValue
    @EnvironmentObject var sessionManager: SessionManager
    @State private var isHovered = false

    private var displayMode: DisplayMode { DisplayMode(rawValue: displayModeRaw) ?? .context }

    /// ↩ shown next to cost when the session's work was later git-reverted (#373).
    @ViewBuilder private var yieldRevertGlyph: some View {
        if session.yieldState == "reverted" {
            Image(systemName: "arrow.uturn.left")
                .font(.system(size: 8, weight: .bold))
                .foregroundColor(IrrColors.pressureHigh)
        }
    }

    /// Cost/CO2 slot content — click to cycle between $ cost and estimated
    /// CO2e (issue #829), keeping the mode-branching out of `body` so it
    /// doesn't add to this already-large view's complexity at each call site.
    /// `isReverted` preserves the existing yield-revert tooltip nuance in
    /// cost mode; CO2 mode always shows its confidence-tier tooltip.
    @ViewBuilder
    private func costOrCO2Label(_ metrics: SessionMetrics, placeholder: String, isReverted: Bool = false) -> some View {
        let showingCO2 = costDisplayModeRaw == "co2"
        let text = showingCO2 ? (metrics.formattedCO2 ?? placeholder) : (metrics.formattedCost ?? placeholder)
        let costTooltip = isReverted
            ? "Estimated cost — session work was reverted — click to show CO2 estimate"
            : "Click to show CO2 estimate"
        let tooltip = showingCO2 ? metrics.co2TierTooltip : costTooltip
        Button(action: { costDisplayModeRaw = showingCO2 ? "cost" : "co2" }) {
            Text(text)
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.secondary)
                .lineLimit(1)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .tooltip(tooltip)
    }

    private var contextThreshold: ContextPressureThreshold {
        ContextPressureThreshold(
            value: contextThresholdValue > 0 ? contextThresholdValue : ContextPressureThreshold.defaultValue,
            unit: ContextPressureThreshold.Unit(rawValue: contextThresholdUnitRaw) ?? ContextPressureThreshold.defaultUnit
        )
    }

    /// Live off the header's global mode and this row's current state
    /// (issue #985) — never snapshotted, so a session that transitions into
    /// `.waiting` while the mode is `.waiting` pops open automatically, and
    /// one that gets answered and moves on collapses again.
    private var summaryCollapsed: Bool {
        switch sessionManager.summaryDisplayMode {
        case .collapsed: return true
        case .waiting: return session.state != .waiting
        }
    }

    /// Session detail block. While waiting, shows the agent's pending question
    /// (orange) — the waiting state is already clear from the row's state icon,
    /// so there's no separate label. It is the only textual pill on the row
    /// (issue #979: the separate purple "user intent" pill was removed — since
    /// it's only ever collected on the same waiting-turn edge as the question,
    /// splitting them into two pills was never a real content distinction). A
    /// `ready` session therefore shows no textual pill, by design. Collapse is
    /// driven globally by the header's collapsed/waiting-only mode; there is no
    /// per-row toggle.
    @ViewBuilder
    private var summaryBlock: some View {
        // Block text is the terse headline (issue #759); the tooltip keeps the
        // full text. Fall back to the full field when the daemon hasn't
        // supplied a headline (older daemon, or pre-headline state). The
        // daemon no longer pre-truncates this to a single-line-sized cut
        // (issue #979), so the pill itself gets more than one line to show it.
        let questionFull = session.state == .waiting ? session.metrics?.lastAssistantText : nil
        let questionLine = session.state == .waiting
            ? (session.metrics?.questionHeadline ?? questionFull)
            : nil
        let showQuestion = (questionLine?.isEmpty == false) && !summaryCollapsed
        if showQuestion, let q = questionLine {
            Text(q)
                .pill(color: IrrColors.waitingPillText, wash: IrrColors.waiting, lineLimit: 3)
                // Surface the full prompt on hover.
                .tooltip(questionFull ?? q)
                .padding(.top, 2)
        }
    }

    /// Cache-creation regression badge (#813, was #374's bare icon+tooltip) —
    /// an always-visible red pill naming the regression so a user isn't
    /// required to hover to learn anything happened. Rendered below the main
    /// row (like summaryBlock) rather than inline among the icon-row badges,
    /// since the short label can be a full version-attribution sentence far
    /// wider than the fixed-width icon slots that row allots each glyph.
    /// Hover still reveals the longer plain-language explanation
    /// (cacheBloatExplanation), composed daemon-side (issue #827) and
    /// rendered verbatim so it can't silently diverge from web's copy.
    @ViewBuilder
    private var cacheBloatBlock: some View {
        if session.metrics?.cacheBloat == true {
            let tooltip = session.metrics?.cacheBloatTooltip
            let base = (tooltip?.isEmpty == false) ? tooltip! : "cache \u{2191}"
            let percent = session.metrics?.cacheBloatPercent ?? 0
            let badgeText = percent > 0 ? "\(base) +\(percent)%" : base
            Text(badgeText)
                .pill(color: IrrColors.pressureHigh, font: .system(size: 9, weight: .semibold, design: .monospaced))
                .padding(.top, 2)
                .tooltip(session.metrics?.cacheBloatExplanation ?? "")
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                // State icon
                SessionStateIcon(state: session.state, size: 12)
                    .tooltip(session.state.label)
                    .accessibilityIdentifier("session-state-icon-\(session.id)")

                // Agent number or role emoji
                if let icon = session.roleIcon, !icon.isEmpty {
                    Text(icon)
                        .font(.system(size: 10))
                        .frame(width: 14, alignment: .center)
                        .tooltip(session.role?.capitalized ?? "")
                } else {
                    Text("\(agentNumber)")
                        .font(.system(size: 9, weight: .medium, design: .monospaced))
                        .foregroundColor(.secondary)
                        .frame(width: 12, alignment: .trailing)
                }

                // Worker name / bead-ID for Gas Town rows (parity with the web
                // worker chips). Gated on `role` so non-orchestrator rows are
                // pixel-identical; bounded width so the branch column downstream
                // doesn't shift across orchestrator rows.
                if session.role != nil {
                    if let wn = session.workerName, !wn.isEmpty {
                        Text(wn)
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.primary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .frame(maxWidth: 60, alignment: .leading)
                    }
                    if let wid = session.workerID, !wid.isEmpty {
                        Text(String(wid.prefix(8)))
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                            .tooltip(wid)
                    }
                }

                // Origin glyph (#538) — a cloud marks a session delivered by a
                // remote relay daemon; local sessions show nothing, so a
                // local-only dashboard is visually unchanged. A session that is
                // also present locally is filtered to the local row upstream
                // (relayOnly), so any row with a daemonID is genuinely remote.
                // Tooltip = the daemon's hostname (from the relay's label map).
                if let daemonID = session.daemonID {
                    let host = sessionManager.relayDaemons[daemonID]
                        ?? sessionManager.offlineDaemons[daemonID] ?? daemonID
                    let offline = sessionManager.isOffline(session)
                    Image(systemName: offline ? "cloud.slash" : "cloud")
                        .font(.system(size: 9))
                        .foregroundColor(.secondary)
                        .frame(width: 14, alignment: .center)
                        .tooltip(offline ? "\(host) — offline" : host)
                }

                // Active subagent count badge
                if activeSubagentCount > 0 {
                    Text("\(activeSubagentCount)")
                        .font(.system(size: 9, weight: .bold, design: .rounded))
                        .foregroundColor(.white)
                        .frame(width: 14, height: 14)
                        .background(IrrColors.working)
                        .clipShape(Circle())
                        .tooltip("\(activeSubagentCount) active subagent\(activeSubagentCount == 1 ? "" : "s")")
                }

                // Background-agent badge (#744) — a moon marks a Claude Code Agent
                // View background agent running detached in the daemon pool after
                // its window closed. Amber "zzz" moon when no window owns it
                // (detached); a muted moon when a window is still open.
                if let bg = session.background {
                    let detached = bg.detached ?? false
                    let trimmedName = (bg.name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                    let label = trimmedName.isEmpty ? "" : " (\(trimmedName))"
                    Image(systemName: detached ? "moon.zzz.fill" : "moon.fill")
                        .font(.system(size: 9))
                        .foregroundColor(detached ? IrrColors.waiting : .secondary)
                        .frame(width: 14, height: 14)
                        .tooltip(detached
                            ? "Detached background agent\(label) — no open window; runs in the Claude Code daemon pool"
                            : "Background agent\(label)")
                }

                // Branch name — column shrinks by one badge's width (14pt + 6pt
                // spacing = 20pt) for EACH leading badge present (subagent count
                // and/or background-agent), so the context-bar column downstream
                // starts at the same x on every row regardless of how many badges
                // a row carries.
                Text(session.gitBranch ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.primary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .frame(width: 64 - CGFloat(20 * ((activeSubagentCount > 0 ? 1 : 0) + (session.background != nil ? 1 : 0))), alignment: .leading)
                    .tooltip(session.gitBranch ?? "—")

                if displayMode == .context {
                    // Fixed-width columns: [bar+tokens_inside 100][cost 36 or % 32]
                    if let metrics = session.metrics, metrics.hasContextData {
                        ContextBar(utilization: metrics.contextUtilization,
                                   pressureColor: metrics.contextPressureColor,
                                   label: metrics.formattedTokenCount)
                            .frame(width: 100, height: 13)
                            .tooltip("Context window usage")
                        if showCostDisplay {
                            HStack(spacing: 1) {
                                yieldRevertGlyph
                                costOrCO2Label(metrics, placeholder: "", isReverted: session.yieldState == "reverted")
                            }
                            .frame(width: 36, alignment: .leading)
                        } else {
                            Text(metrics.formattedContextUtilization)
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundColor(metrics.contextPressureColor)
                                .frame(width: 32, alignment: .leading)
                        }
                    } else if let metrics = session.metrics, metrics.totalTokens > 0 {
                        // Tokens flowing but no context-window data — daemon
                        // sets metrics.contextWindowUnknown when the capacity
                        // manager has no LiteLLM pricing entry for the model
                        // (common for aider via LM Studio / any local
                        // provider). Render the raw token count in the bar
                        // slot so the row carries signal, and put cost (or
                        // a placeholder) in the secondary column.
                        Text(metrics.formattedTokenUsage)
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundColor(.secondary)
                            .frame(width: 100, height: 13, alignment: .leading)
                            .tooltip("Token count — context window not known for \(session.shortModelName)")
                        if showCostDisplay {
                            HStack(spacing: 1) {
                                yieldRevertGlyph
                                costOrCO2Label(metrics, placeholder: "—", isReverted: session.yieldState == "reverted")
                            }
                            .frame(width: 36, alignment: .leading)
                        } else {
                            Text("—")
                                .font(.system(size: 9, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 32, alignment: .leading)
                        }
                    } else {
                        Color.clear.frame(width: 132, height: 13)
                    }
                } else {
                    // Historical modes (1s/10s/60s): bar fills the same column as the
                    // Context bar+label so x-alignment stays stable across modes, and is
                    // taller because it carries no cost/% readout alongside it.
                    HistoryBarView(states: sessionManager.stateHistory[session.id] ?? [],
                                   bucketCount: sessionManager.historyBucketCount)
                        .frame(width: 132, height: 16)
                        .tooltip(displayMode.tooltip)
                }

                Spacer()

                if debugMode {
                    SessionActionButtons(session: session)
                }

                // Backchannel control affordance (#724): only when the daemon
                // says this session is controllable (toggle on + consent + a
                // usable terminal backend).
                if session.controllable == true {
                    SessionControlButton(session: session)
                }

                // Short model name + adapter icon — grouped so layoutPriority applies to both
                HStack(spacing: 6) {
                    Text(session.shortModelName)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .tooltip(session.effectiveModel)
                        .accessibilityIdentifier("session-model-label-\(session.id)")
                    if let icon = session.adapterIcon {
                        Image(nsImage: icon)
                            .frame(width: 12, height: 12)
                            .tooltip(session.adapterName)
                    }
                }
                .layoutPriority(1)
            }
            // Pin row to the tallest bar (history at 16pt) so toggling between
            // Context and 1s/10s/60s doesn't shift row height.
            .frame(minHeight: 16)

            // Task summary + waiting question — a single collapsible block
            // (issue #738). The summary ("what is this session about") shows
            // in any state; the question shows only while waiting. The list
            // header's collapse-all toggle governs every row at once (global
            // state, no per-row chevron). Default expanded so the info is visible.
            summaryBlock

            // Cache-creation regression badge (#813) — always visible, unlike
            // summaryBlock, since it flags a cost regression rather than
            // optional context and isn't subject to the collapse-all toggle.
            cacheBloatBlock

            // Context pressure alert (configurable threshold, active sessions only — #689)
            if let metrics = session.metrics,
               (session.state == .working || session.state == .waiting),
               contextThreshold.isExceeded(by: metrics) {
                let alertColor = IrrColors.pressureHigh
                HStack(spacing: 4) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.system(size: 9))
                        .foregroundColor(alertColor)
                    Text("Switch to a fresh session soon")
                        .font(.system(size: 9))
                        .foregroundColor(alertColor)
                    Spacer()
                }
                .padding(.horizontal, 4)
                .padding(.vertical, 2)
                .background(alertColor.opacity(0.08))
                .cornerRadius(IrrRadius.sm)
                .padding(.top, 2)
                .tooltip("Context window nearing limit")
            }

            // Task progress + completion ETA share one line: dots left, ETA
            // right (issue #558).
            taskProgressRow

            // Debug info
            if debugMode {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    HStack(spacing: 8) {
                        Text(session.shortId)
                            .onTapGesture {
                                NSPasteboard.general.clearContents()
                                NSPasteboard.general.setString(session.id, forType: .string)
                            }
                            .tooltip("Click to copy full ID")
                        Text("updated: \(elapsedString(from: session.updatedAt, now: context.date))")
                        Text("created: \(elapsedString(from: session.firstSeen, now: context.date))")
                        if let metrics = session.metrics, metrics.totalTokens > 0 {
                            Text("ctx: \(metrics.formattedTokenUsage)")
                        }
                        Spacer()
                    }
                    .font(.system(size: 9, design: .monospaced))
                    .foregroundColor(.secondary.opacity(0.7))
                    .padding(.top, 2)
                }
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 4)
        // Fade, don't delete (#540): a row whose relay daemon has gone offline
        // dims in place and restores on reconnect, so a flapping link doesn't
        // yank rows. Local sessions are never offline.
        .opacity(sessionManager.isOffline(session) ? 0.4 : 1)
        .animation(IrrMotion.easeOut(duration: IrrMotion.fast), value: sessionManager.isOffline(session))
        .background(isHovered ? IrrColors.surfaceHover : Color.clear)
        .contentShape(Rectangle())
        .onHover { hovering in
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                isHovered = hovering
            }
        }
        .onTapGesture {
            SessionLauncher.jump(session)
        }
        .accessibilityIdentifier("session-card-\(session.id)")
        .accessibilityLabel("\(session.projectName ?? "unknown") \(session.state.rawValue) \(session.shortModelName)")
        .accessibilityAddTraits(.isButton)
        .accessibilityHint("Brings the session's terminal or editor to the foreground")
    }

    private func elapsedString(from date: Date, now: Date) -> String {
        let total = max(0, Int(now.timeIntervalSince(date)))
        let h = total / 3600
        let m = (total % 3600) / 60
        let s = total % 60
        if h > 0 {
            return String(format: "%d:%02d:%02d", h, m, s)
        }
        return String(format: "%d:%02d", m, s)
    }

    /// The session's task list when it should render — non-empty and not
    /// fully completed (same gate the standalone dots row used).
    private var activeTasks: [SessionTask]? {
        guard let tasks = session.metrics?.tasks, !tasks.isEmpty, !tasks.allSatisfy(\.isCompleted) else { return nil }
        return tasks
    }

    private struct TaskEtaPresentation {
        let text: String
        let stale: Bool
        let title: String
    }

    /// One sub-row: task dots left, completion ETA right (issue #558). It's a
    /// sub-row rather than part of the main HStack because the ETA, placed
    /// inline next to the cost, truncated to "…" at menu-bar width (the model
    /// label's layoutPriority wins the squeeze); the ETA label is fixed-size
    /// so the wrapping dots can't compress it. taskEtaPresentation() is
    /// computed exactly once here.
    @ViewBuilder
    private var taskProgressRow: some View {
        let eta = taskEtaPresentation()
        if activeTasks != nil || eta != nil {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                if let tasks = activeTasks {
                    TaskListView(tasks: tasks)
                }
                Spacer(minLength: 8)
                if let eta, let est = session.metrics?.taskEstimate {
                    // Progress as a percentage — the raw rounds (5/10) read
                    // like a second task counter next to the dots; the tooltip
                    // still carries the exact rounds.
                    let percent = Int((Double(est.completedRounds) / Double(max(est.totalRounds, 1)) * 100).rounded())
                    HStack(spacing: 4) {
                        Image(systemName: "timer")
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                        Text("\(eta.text) · \(percent)%")
                            .font(.system(size: 9, design: .monospaced))
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                    }
                    .fixedSize()
                    .opacity(eta.stale ? 0.5 : 1.0)
                    .tooltip(eta.title)
                }
            }
            .padding(.top, 2)
        }
    }

    /// Decides the task-completion ETA chip (issue #558) — mirrors the web's
    /// taskEtaPresentation. Nil hides the chip: session not working or no
    /// estimate. Zero completed rounds renders a progress-only "estimating…"
    /// chip (#602); with progress, a range whose high bound stays pinned at
    /// the last marker — 1.5× padded below half the rounds, bare at/above
    /// half so it collapses to a point right at a marker and widens instead
    /// of counting down (#616) — and stale dimming when the last marker is
    /// older than 3 minutes.
    private func taskEtaPresentation(now: Date = Date()) -> TaskEtaPresentation? {
        guard session.state == .working,
              let metrics = session.metrics,
              let est = metrics.taskEstimate else { return nil }
        let sourceLabel: String
        switch est.source {
        case "tasks": sourceLabel = "from task list"
        case "subagents": sourceLabel = "from subagents"
        default: sourceLabel = "agent-reported"
        }
        guard est.completedRounds > 0 else {
            // No MEASURED rate yet, but the daemon projects from a corpus prior
            // (#753) so a real number shows at the first marker instead of
            // "estimating…" (#602). Widen the range (2×) to signal a population
            // prior, not a measured rate; no projection → "estimating…".
            guard est.totalRounds > 0 else { return nil }
            var stale = false
            var title = "Task ETA — \(sourceLabel) 0/\(est.totalRounds) rounds"
            if let updated = est.updatedAt {
                let age = max(0, now.timeIntervalSince(updated))
                stale = age > 180
                title += ", updated \(Int(age))s ago"
            }
            guard let eta = metrics.taskCompletionEta else {
                return TaskEtaPresentation(text: "estimating…", stale: stale, title: title)
            }
            let rem0 = max(0, eta.timeIntervalSince(now))
            let high0: TimeInterval
            if let updated = est.updatedAt {
                high0 = max(rem0, eta.timeIntervalSince(updated) * 2)
            } else {
                high0 = rem0 * 2
            }
            let text0 = etaText(remaining: rem0, highSecs: high0)
            return TaskEtaPresentation(text: text0, stale: stale, title: title + " · rough prior")
        }
        guard let eta = metrics.taskCompletionEta else {
            // Progress without a projection (e.g. a subagent aggregate whose
            // children carry no etas yet, #626): show a rounds-only chip
            // rather than hiding one that was visible moments ago.
            var stale = false
            var title = "Task ETA — \(sourceLabel) \(est.completedRounds)/\(est.totalRounds) rounds"
            if let updated = est.updatedAt {
                let age = max(0, now.timeIntervalSince(updated))
                stale = age > 180
                title += ", updated \(Int(age))s ago"
            }
            return TaskEtaPresentation(
                text: "\(est.completedRounds)/\(est.totalRounds)", stale: stale, title: title)
        }
        let remaining = max(0, eta.timeIntervalSince(now))
        let frac = est.totalRounds > 0 ? Double(est.completedRounds) / Double(est.totalRounds) : 0
        // The eta is anchored at the marker (daemon-side): the LOW bound
        // counts down between marker updates while the HIGH bound stays
        // pinned at the marker until the agent reports fresh progress —
        // 1.5× the projected remaining time while the rate is barely
        // measurable (below half the rounds), the bare projected remaining
        // once it's trusted, so the point estimate widens instead of
        // counting down naked (#616). No marker timestamp at/above half →
        // nothing to pin to, keep the point estimate.
        let factor = frac < 0.5 ? 1.5 : 1.0
        var highSecs: TimeInterval? = nil
        if let updated = est.updatedAt {
            highSecs = max(remaining, eta.timeIntervalSince(updated) * factor)
        } else if frac < 0.5 {
            highSecs = remaining * 1.5
        }
        let text = etaText(remaining: remaining, highSecs: highSecs)
        var stale = false
        var title = "Task ETA — \(sourceLabel) \(est.completedRounds)/\(est.totalRounds) rounds"
        if let updated = est.updatedAt {
            let age = max(0, now.timeIntervalSince(updated))
            stale = age > 180
            title += ", updated \(Int(age))s ago"
        }
        return TaskEtaPresentation(text: text, stale: stale, title: title)
    }

    /// Renders the remaining-time text with exactly ONE sign — "~"
    /// (approximate) or "<" (upper bound), never both, never a degenerate
    /// "2m–2m" range (mirrors the web's fmtEtaText). highSecs nil → point.
    ///   point, ≥1m → "~12m left" · point, <1m → "<1m left"
    ///   range, low <1m → "<2m left" (collapses to its upper bound)
    ///   range, low==high → point rules · range → "~8m–12m left"
    private func etaText(remaining: TimeInterval, highSecs: TimeInterval?) -> String {
        let low = etaDurationString(remaining)
        if let highSecs {
            let high = etaDurationString(highSecs)
            if low != high {
                if remaining < 60 { return "<\(high) left" }
                return "~\(low)–\(high) left"
            }
        }
        if remaining < 60 { return "<1m left" }
        return "~\(low) left"
    }

    /// Minute-resolution duration for the ETA chip — second-level detail
    /// would flicker for a number that is inherently rough.
    private func etaDurationString(_ seconds: TimeInterval) -> String {
        if seconds < 60 { return "<1m" }
        let mins = Int((seconds / 60).rounded())
        let h = mins / 60
        let m = mins % 60
        if h > 0 { return m > 0 ? "\(h)h\(m)m" : "\(h)h" }
        return "\(m)m"
    }
}

// MARK: - Task Progress

/// Wraps children left-to-right, starting a new row when the available width is exhausted.
private struct FlowLayout: Layout {
    var hSpacing: CGFloat = 4
    var vSpacing: CGFloat = 3

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache _: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            if x + size.width > maxWidth && x > 0 {
                y += rowHeight + vSpacing
                x = 0
                rowHeight = 0
            }
            x += size.width + hSpacing
            rowHeight = max(rowHeight, size.height)
        }
        return CGSize(width: maxWidth, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal _: ProposedViewSize, subviews: Subviews, cache _: inout ()) {
        // First pass: group subviews into rows so we know each row's
        // height before placing items. Second pass: place items with
        // their vertical center aligned to the row center, so tiny
        // circles and the taller "done/total" label line up.
        var rows: [[(sub: LayoutSubview, size: CGSize)]] = [[]]
        var currentRowWidth: CGFloat = 0
        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            let needsWrap = currentRowWidth + size.width > bounds.width && !rows[rows.count - 1].isEmpty
            if needsWrap {
                rows.append([])
                currentRowWidth = 0
            }
            rows[rows.count - 1].append((sub, size))
            currentRowWidth += size.width + hSpacing
        }

        var y = bounds.minY
        for row in rows {
            let rowHeight = row.map(\.size.height).max() ?? 0
            var x = bounds.minX
            for (sub, size) in row {
                let yCentered = y + (rowHeight - size.height) / 2
                sub.place(at: CGPoint(x: x, y: yCentered), proposal: .unspecified)
                x += size.width + hSpacing
            }
            y += rowHeight + vSpacing
        }
    }
}

/// Compact dot-progress row: one circle per task (filled = done, empty = pending) + "4 / 6" count.
/// Dots wrap to the next line when the row is full.
private struct TaskListView: View {
    let tasks: [SessionTask]

    var body: some View {
        let done = tasks.filter(\.isCompleted).count
        FlowLayout(hSpacing: 4, vSpacing: 3) {
            ForEach(tasks, id: \.id) { task in
                Group {
                    if task.isCompleted {
                        Circle().fill(IrrColors.ready.opacity(0.85))
                    } else {
                        Circle().strokeBorder(IrrColors.working, lineWidth: 1.5)
                    }
                }
                .frame(width: 7, height: 7)
                .tooltip(task.displayLabel)
            }
            Text("\(done) / \(tasks.count)")
                .font(.system(size: 9))
                .foregroundColor(.secondary)
                .padding(.leading, 2)
        }
    }
}

// MARK: - Session Action Buttons

/// Backchannel control affordance (#724): a keyboard button that opens a
/// popover to send text or an interrupt into a controllable session. Shown only
/// when `session.controllable`. The whole-row tap (focus) is unaffected —
/// SwiftUI gives the button its own tap handling.
struct SessionControlButton: View {
    let session: SessionState
    @EnvironmentObject var sessionManager: SessionManager
    @State private var showPopover = false
    @State private var draft = ""

    var body: some View {
        Button {
            showPopover.toggle()
        } label: {
            Image(systemName: "keyboard")
                .font(.system(size: 10))
                .foregroundColor(.secondary)
        }
        .buttonStyle(.plain)
        .tooltip("Send input to this session")
        .accessibilityIdentifier("session-control-\(session.id)")
        .popover(isPresented: $showPopover, arrowEdge: .bottom) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Send to session").font(.caption).foregroundColor(.secondary)
                HStack(spacing: 6) {
                    TextField("text or /command", text: $draft)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 220)
                        .onSubmit(send)
                    Button("Send", action: send).disabled(draft.isEmpty)
                }
                Button(role: .destructive) {
                    Task { _ = await sessionManager.interruptSession(sessionId: session.id) }
                    showPopover = false
                } label: {
                    Label("Interrupt (Ctrl-C)", systemImage: "stop.circle")
                }
                .buttonStyle(.borderless)
            }
            .padding(12)
        }
    }

    private func send() {
        let text = draft
        guard !text.isEmpty else { return }
        // Append a return so the line submits (mirrors the tmux send-keys path).
        Task { _ = await sessionManager.sendInput(sessionId: session.id, text: text + "\r") }
        draft = ""
        showPopover = false
    }
}

struct SessionActionButtons: View {
    let session: SessionState
    @EnvironmentObject var sessionManager: SessionManager

    var body: some View {
        HStack(spacing: 4) {
            Button(action: {
                sessionManager.resetSessionState(sessionId: session.id)
            }) {
                Image(systemName: "arrow.counterclockwise")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .tooltip("Reset to ready state")

            Button(action: {
                sessionManager.deleteSession(sessionId: session.id)
            }) {
                Image(systemName: "trash")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .tooltip("Delete session")
        }
        .opacity(0.6)
    }
}
