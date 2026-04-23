import SwiftUI

// Colors match the web frontend palette and SessionState.State.color.
private let stateColors: [String: Color] = [
    "working": Color(hex: "#8B5CF6"),
    "waiting": Color(hex: "#FF9500"),
    "ready":   Color(hex: "#34C759"),
]

/// A compact horizontal bar that visualises per-session state history.
/// Each bucket is a single coloured rectangle: purple=working, orange=waiting, green=ready.
struct HistoryBarView: View {
    let states: [String]  // oldest → newest

    var body: some View {
        Canvas { context, size in
            guard !states.isEmpty else { return }
            let count = states.count
            let colW = size.width / CGFloat(count)
            for (i, state) in states.enumerated() {
                let color = stateColors[state] ?? stateColors["ready"]!
                let rect = CGRect(
                    x: CGFloat(i) * colW,
                    y: 0,
                    width: max(colW - 0.5, 0.5),
                    height: size.height
                )
                context.fill(Path(rect), with: .color(color))
            }
        }
        .background(states.isEmpty ? Color.secondary.opacity(0.12) : .clear)
        .clipShape(RoundedRectangle(cornerRadius: 2))
    }
}
