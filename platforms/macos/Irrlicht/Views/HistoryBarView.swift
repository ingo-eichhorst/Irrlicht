import SwiftUI

private let stateColors: [String: Color] = [
    "working": IrrColors.working,
    "waiting": IrrColors.waiting,
    "ready":   IrrColors.ready,
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
            // Right-anchor: draw the newest `bucketCount` states; when fewer
            // exist, leave the LEFT slots empty.
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
        .background(IrrColors.trackFill)
        .clipShape(RoundedRectangle(cornerRadius: IrrRadius.xs))
    }
}
