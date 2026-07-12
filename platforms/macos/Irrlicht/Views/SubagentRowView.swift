import AppKit
import SwiftUI

// MARK: - Subagent Row View

struct SubagentRowView: View {
    let session: SessionState
    @AppStorage("debugMode") private var debugMode: Bool = false
    @State private var isHovered = false

    var body: some View {
        HStack(spacing: 6) {
            // Indentation
            Spacer().frame(width: 24)

            // State indicator (small)
            SessionStateIcon(state: session.state, size: 12)
                .tooltip(session.state.label)

            // Context utilization
            if let metrics = session.metrics, metrics.hasContextData {
                Text(metrics.formattedContextUtilization)
                    .font(.caption2)
                    .foregroundColor(metrics.contextPressureColor)
            } else if debugMode, let metrics = session.metrics, metrics.totalTokens > 0 {
                Text(metrics.formattedTokenUsage)
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            } else {
                Text("—")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            }

            Spacer()

            // Duration — driven by the shared 1 Hz DurationClock so N subagent
            // rows cost one timer, not N independent TimelineView schedulers (#690).
            if let metrics = session.metrics {
                let isActive = session.state == .working || session.state == .waiting
                LiveDurationText(isActive: isActive, metrics: metrics, firstSeen: session.firstSeen)
            } else {
                Text("—")
                    .font(.caption2)
                    .foregroundColor(.secondary.opacity(0.6))
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 3)
        .background(isHovered ? IrrColors.surfaceHoverSubtle : Color.clear)
        .onHover { hovering in
            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                isHovered = hovering
            }
        }
        .accessibilityIdentifier("subagent-card-\(session.id)")
        .accessibilityLabel("subagent \(session.state.rawValue)")
    }
}

// MARK: - Shared duration clock (#690)

/// One 1 Hz clock shared by every live duration label. Replaces the per-row
/// `TimelineView(.periodic)` schedulers so N visible rows cost a single timer
/// instead of N. Only the leaf `LiveDurationText` views observe it, so a tick
/// re-renders just those labels — never their parent rows.
@MainActor
final class DurationClock: ObservableObject {
    static let shared = DurationClock()
    /// Bumped every second purely to invalidate observing labels; the duration
    /// strings read the wall clock themselves, so the value is never consumed.
    @Published private(set) var tick: UInt64 = 0
    private var timer: Timer?

    private init() {
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.tick &+= 1 }
        }
    }
}

/// A duration label that refreshes once per second off the shared `DurationClock`.
/// Active sessions show realtime elapsed; finished ones show their frozen total.
private struct LiveDurationText: View {
    @ObservedObject private var clock = DurationClock.shared
    let isActive: Bool
    let metrics: SessionMetrics
    let firstSeen: Date

    var body: some View {
        let staticElapsedText = metrics.elapsedSeconds > 0 ? metrics.formattedElapsedTime : "—"
        Text(isActive
            ? metrics.formattedRealtimeElapsedTime(sessionFirstSeen: firstSeen)
            : staticElapsedText)
            .font(.caption2)
            .foregroundColor(.secondary.opacity(0.7))
    }
}
