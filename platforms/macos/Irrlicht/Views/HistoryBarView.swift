import SwiftUI

// Colors match the web frontend palette and SessionState.State.color.
private let stateColors: [String: Color] = [
    "working": Color(hex: "#8B5CF6"),
    "waiting": Color(hex: "#FF9500"),
    "ready":   Color(hex: "#34C759"),
]

/// A compact horizontal bar that visualises per-session state history.
/// Each bucket is a fixed-width coloured rectangle: purple=working, orange=waiting, green=ready.
/// Populated buckets accumulate left-to-right; the unfilled tail uses the background colour.
struct HistoryBarView: View {
    let states: [String]     // oldest → newest
    var bucketCount: Int = 150

    var body: some View {
        Canvas { context, size in
            guard !states.isEmpty else { return }
            let colW = size.width / CGFloat(bucketCount)
            for (i, state) in states.enumerated() {
                let color = stateColors[state] ?? stateColors["ready"]!
                let rect = CGRect(
                    x: CGFloat(i) * colW,
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
