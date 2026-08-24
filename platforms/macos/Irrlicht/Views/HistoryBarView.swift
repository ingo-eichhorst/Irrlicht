import SwiftUI

/// A compact horizontal bar that visualises per-session state history.
/// Each bucket is a fixed-width coloured rectangle: purple=working, orange=waiting, green=ready.
/// Buckets are right-anchored: the newest state always lands in the rightmost
/// column, and as time passes older buckets shift leftward.
struct HistoryBarView: View {
    let states: [String]     // oldest → newest
    var bucketCount: Int = 150

    /// Bucket name → fill, or nil for "paint nothing here".
    ///
    /// Extracted from `body` so the #1797 rule is assertable at all: a `Canvas`
    /// closure is not reachable from a test, and the alternative — an image
    /// snapshot — is reference-host-only (docs/swift-testing.md), so this
    /// decision would otherwise have had zero gating coverage.
    ///
    /// Derived from `SessionState.State` rather than a private lookup table, so
    /// a bucket and the row's state icon cannot drift apart; the table this
    /// replaced was a third independent copy of the state→color mapping.
    ///
    /// Two non-states reach here, and neither may paint green (#1797):
    ///   - `""`, the no-data code (`historyPriorityToState` returns it for wire
    ///     code 3). `trimLeadingNoData` strips only LEADING empties, so a gap
    ///     anywhere else used to be painted READY GREEN, inventing a finished
    ///     session out of missing data. The web twin has always skipped it
    ///     (`irrlicht.js`: `if (!color) continue`), so the two platforms
    ///     rendered the same ring differently. THIS is the reachable defect.
    ///   - any other unrecognized name → neutral grey. DEFENSIVE ONLY: every
    ///     string that can reach `states` comes from `historyPriorityToState`,
    ///     a 4-way switch over a 2-bit code that returns only
    ///     ready/working/waiting/"" — so a newer daemon's state arrives here as
    ///     a priority CODE, not a name, and the encoding has no spare code for
    ///     it. Widening that wire format is the epic's problem (#1807), not
    ///     something this branch can fix.
    static func barColor(for state: String) -> Color? {
        if state.isEmpty { return nil }          // no-data slot
        return (SessionState.State(rawValue: state) ?? .unknown).color
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
                // #1797 — see barColor. A nil means "paint nothing", which also
                // skips the CGRect/Path/fill entirely for gap buckets.
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
