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

    /// Bucket name → fill, or nil for "paint nothing here".
    ///
    /// Extracted from `body` so the #1797 rule is assertable: a `Canvas`
    /// closure is not reachable from a test, and this decision — which is the
    /// entire defect — would otherwise be provable only by a rendered image.
    static func barColor(for state: String) -> Color? {
        if state.isEmpty { return nil }          // no-data slot
        return stateColors[state] ?? IrrColors.unknown
    }

    var body: some View {
        Canvas { context, size in
            guard !states.isEmpty else { return }
            let colW = size.width / CGFloat(bucketCount)
            // Right-anchor: draw the newest `bucketCount` states; when fewer
            // exist, leave the LEFT slots empty.
            let visible = states.suffix(bucketCount)
            let offset = bucketCount - visible.count
            for (i, state) in visible.enumerated() {
                // #1797. Two distinct non-states, and neither may paint green:
                //   - "" is the no-data code (historyPriorityToState returns it
                //     for wire code 3). trimLeadingNoData strips only LEADING
                //     empties, so a mid-array one reaches here — and used to be
                //     painted READY GREEN, inventing a finished session out of
                //     a gap. Leave the slot unpainted, which is what the doc
                //     comment on historyPriorityToState already claims happens
                //     and what the web twin does (irrlicht.js: `if (!color)
                //     continue`).
                //   - any other unrecognized name is a state this build cannot
                //     read: neutral grey, same as the row's state icon.
                guard let color = Self.barColor(for: state) else { continue }
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
