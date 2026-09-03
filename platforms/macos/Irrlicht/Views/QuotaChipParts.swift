import SwiftUI

/// The two pieces of the quota chip whose rendering depends on the wall clock
/// (#1675), each extracted into a view of its own for the reason #1663
/// extracted `QuotaResetLabel`: it is the only way the site can be rendered by
/// a test.
///
/// ## Why an extraction was unavoidable
///
/// Both reads live inside `SessionListView`. `quotaWindowRow` and
/// `quotaChipView` are methods on it, so reaching either means hosting the
/// whole 380pt panel with its three environment objects — and an
/// `@Environment` read off a `SessionListView` value a test constructed
/// itself, outside a view update, answers the DEFAULT. That is a pin reaching
/// nothing wearing the shape of a passing test, which is exactly what #1663
/// recorded when it hit the same wall one read earlier.
///
/// So the clock read moves to the smallest view that owns the pixels it
/// changes. `QuotaWindowRow` owns the pace marker's x position;
/// `QuotaStaleDimmed` owns the chip's opacity. Neither adds or removes a
/// modifier, and no committed reference renders either site (re-measured on
/// #1874: `git grep -n "rateLimit" -- platforms/macos/Tests` matches only a
/// doc comment, and the directories under `Tests/__Snapshots__` —
/// `find Tests/__Snapshots__ -mindepth 1 -type d | wc -l` → 6, holding
/// `find Tests/__Snapshots__ -name '*.png' | wc -l` → 60 references — belong
/// to suites that render session rows, groups, history, the wizard and the two
/// banners, none the panel header), so that reference set is untouched by
/// construction rather than by inspection.
///
/// ## What each one is graded by
///
/// `QuotaChipClockTests` drives two pinned clocks through each of these views
/// via `PinnedSnapshotHost` and refuses an identical rasterisation. That
/// comparison is the only thing that catches the mutation this change exists
/// to make catchable — putting `Date()` back at the call site — because every
/// string- and value-level assertion stays green under it (#1676 measured
/// exactly that for the reset label, one read earlier).

/// One row inside a subscription chip: window label, the bar with its pace
/// marker, the used percent, and — outside compact mode — the reset label.
///
/// In compact mode (multiple chips visible) the inline reset time is dropped —
/// it lives in the tooltip — and the bar shrinks so two or three chips fit in
/// the 380pt header.
///
/// Was `SessionListView.quotaWindowRow`. The `Date()` it used to read for the
/// pace marker is now this view's `\.formatNow`, read once for the row: the old
/// in-place comment worried that calling `quotaPacePercent` twice would let two
/// `Date()` captures disagree by microseconds, and a stopped clock removes that
/// hazard rather than routing around it.
struct QuotaWindowRow: View {
    let window: RateLimitWindowInfo
    let compact: Bool

    @Environment(\.formatNow) private var formatNow

    var body: some View {
        // Computed once per row, from one read of the clock — SwiftUI
        // re-invokes view bodies on every SessionManager publish.
        let pace = SessionListView.quotaPacePercent(window, now: formatNow())
        HStack(spacing: 6) {
            Text(SessionListView.quotaWindowLabel(window.windowMinutes))
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.secondary)
                .frame(width: 14, alignment: .leading)

            QuotaWindowRow.bar(percent: window.usedPercent,
                               color: SessionListView.barColor(used: window.usedPercent, pace: pace),
                               pacePercent: pace)
                .frame(width: compact ? 60 : 70, height: 5)

            Text("\(Int(window.usedPercent.rounded()))%")
                .font(.system(size: 9, weight: .medium, design: .monospaced))
                .foregroundColor(.primary)
                .frame(width: 28, alignment: .trailing)

            if !compact {
                // A view of its own since #1663 — see `QuotaResetLabel`, which
                // reads the four values this rendering used to take off the
                // machine (now, calendar, zone, locale) from the environment.
                QuotaResetLabel(resetsAt: window.resetsAt)
            }
        }
    }

    /// A rounded-rect progress bar with an optional vertical pace marker (thin
    /// red line at `pacePercent`). The marker reads "where you should be if
    /// you've been pacing evenly" — fill past the marker means burning quota
    /// faster than the window's linear rate; fill behind it means headroom.
    /// ZStack so the fill, track, and marker render with the same corner radius
    /// without clipping artifacts at small sizes.
    ///
    /// Takes `pacePercent` as an argument and reads no clock: it is the pixels,
    /// not the decision. Was `SessionListView.quotaBar`, unchanged.
    @ViewBuilder
    static func bar(percent: Double, color: Color, pacePercent: Double?) -> some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(Color.secondary.opacity(0.2))
                RoundedRectangle(cornerRadius: 2.5)
                    .fill(color)
                    .frame(width: geo.size.width * min(1.0, max(0.0, percent / 100.0)))
                if let pace = pacePercent, (0...100).contains(pace) {
                    Rectangle()
                        .fill(Color.red)
                        .frame(width: 1)
                        .offset(x: geo.size.width * (pace / 100.0) - 0.5)
                }
            }
        }
    }
}

/// Dims a quota chip whose snapshot pre-dates the current window.
///
/// The chip's staleness test is a **discrete** function of the clock, which is
/// what makes it the worse of #1675's two pixel-visible reads: a reference
/// recorded a minute before a fixture's `resetsAt` is correct, and permanently
/// wrong a minute later. #1663 met the same shape in `formatResetTime`'s
/// same-day branch.
///
/// The verdict is decided HERE, from this subtree's `\.formatNow`, rather than
/// carried in from `mergeIntoBuckets` as it was before #1675. Two reasons, and
/// the second is the load-bearing one:
///
/// - It is not a second source of truth. `SessionListView.snapshotIsStale` is
///   the one implementation, and the merge's own verdict equals what this
///   re-derives for every path through `mergeIntoBuckets` — the bucket only
///   ever keeps `isStale` alongside the snapshot it was computed from. That is
///   locked by
///   `QuotaChipClockTests.testTheMergeNeverKeepsASnapshotWhoseStalenessDisagreesWithIt`
///   rather than left as an argument.
/// - A verdict computed in `SessionListView.quotaChipData` is one no test can
///   drive, for the `@Environment`-answers-the-default reason at the top of
///   this file. Computed here, it is a function of a pinnable environment
///   value, so two clocks through one view produce two rasterisations — the
///   only assertion shape that catches a `Date()` put back at the call site.
///
/// One consequence, stated rather than buried: under the *running* default
/// clock this read and the merge's read are microseconds apart, so at a
/// `resetsAt` boundary a chip can dim one frame before its tooltip says
/// "stale". Both self-correct on the next `SessionManager` publish, and under
/// any pinned clock they are the same instant. That is the same
/// microsecond-disagreement class `quotaWindowRow` already documented for two
/// `quotaPacePercent` calls, traded for a decision a test can reach.
struct QuotaStaleDimmed<Content: View>: View {
    let snapshot: RateLimitInfo
    @ViewBuilder let content: Content

    @Environment(\.formatNow) private var formatNow

    /// The opacity a stale chip renders at. Named so a test asserts against the
    /// production constant rather than a copy of it.
    static var staleOpacity: Double { 0.5 }

    var body: some View {
        content
            .opacity(SessionListView.snapshotIsStale(snapshot, now: formatNow())
                     ? Self.staleOpacity : 1.0)
    }
}
