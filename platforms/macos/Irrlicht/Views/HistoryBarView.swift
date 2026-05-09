import SwiftUI

// Colors match the web frontend palette and SessionState.State.color.
private let stateColors: [String: Color] = [
    "working": Color(hex: "#8B5CF6"),
    "waiting": Color(hex: "#FF9500"),
    "ready":   Color(hex: "#34C759"),
]

/// A compact horizontal bar that visualises per-session state history.
/// Each bucket is a fixed-width coloured rectangle: purple=working, orange=waiting, green=ready.
/// Buckets are right-anchored: the newest state always lands in the rightmost
/// column, and as time passes older buckets shift leftward.
struct HistoryBarView: View {
    let states: [String]     // oldest → newest
    var bucketCount: Int = 150

    var body: some View {
        Canvas { context, size in
            guard !states.isEmpty else { return }
            let colW = size.width / CGFloat(bucketCount)
            // Right-align: keep only the newest `bucketCount` states so the
            // most recent always lands at the rightmost column. When fewer
            // states than buckets exist, leave the LEFT slots unfilled so the
            // background colour shows through.
            let visible = states.suffix(bucketCount)
            let offset = bucketCount - visible.count
            for (i, state) in visible.enumerated() {
                let color = stateColors[state] ?? stateColors["ready"]!
                let rect = CGRect(
                    x: CGFloat(offset + i) * colW,
                    y: 0,
                    width: max(colW, 0.5),
                    height: size.height
                )
                context.fill(Path(rect), with: .color(color))
            }
        }
        .background(Color.secondary.opacity(0.12))
        .clipShape(RoundedRectangle(cornerRadius: 2))
    }
}
