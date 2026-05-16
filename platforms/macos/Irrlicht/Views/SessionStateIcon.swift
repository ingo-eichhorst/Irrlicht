import SwiftUI

/// Per-row session-state glyph, unified with the web dashboard
/// (`platforms/web/index.html` `svgIcons`):
///
/// - working: large solid dot (r = 7/20 of frame), opacity-only breathe
///            between 0.55 and 1.0 over 1.4 s.
/// - waiting: two-bar pause.
/// - ready:   SF Symbol `checkmark.circle.fill` (unchanged from before).
///
/// The opacity animation is suppressed when the user has enabled Reduce
/// Motion; the dot remains at full opacity, fully visible.
struct SessionStateIcon: View {
    let state: SessionState.State
    let size: CGFloat

    var body: some View {
        switch state {
        case .working:
            WorkingIcon(size: size)
        case .waiting:
            WaitingIcon(size: size)
        case .ready:
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: size))
                .foregroundColor(IrrColors.ready)
        }
    }
}

/// Breathing solid dot — opacity-only breathe (0.55 ↔ 1.0 over 1.4 s,
/// ease-in-out, autoreversing). The dot is sized to match the ready-state
/// SF Symbol `checkmark.circle.fill` (full frame, no inset) so working and
/// ready read as the same size in the row. No scaling animation — the dot
/// stays at its full footprint even if the animation drops frames or stops,
/// so the worst-case render is a large solid purple disc the same size as
/// the ready check.
///
/// First render is at full opacity (1.0) — `dim` starts `false`, then the
/// `withAnimation` block in `.onAppear` flips it to true with an explicit
/// `repeatForever(autoreverses:)` curve. Using `withAnimation` here instead
/// of the implicit `.animation(value:)` modifier is load-bearing: the
/// implicit form is cancelled when a parent view re-renders (which the
/// session row does frequently, on every WebSocket push), so the breathe
/// silently freezes after the first row update. `withAnimation` scopes the
/// curve to the state change itself and survives parent re-renders.
private struct WorkingIcon: View {
    let size: CGFloat
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    private static let period: Double = 1.4
    private static let dimOpacity: Double = 0.55

    var body: some View {
        Circle()
            .fill(IrrColors.working)
            .frame(width: size, height: size)
            .opacity(reduceMotion ? 1 : (dim ? Self.dimOpacity : 1))
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(
                    .easeInOut(duration: Self.period / 2)
                        .repeatForever(autoreverses: true)
                ) {
                    dim = true
                }
            }
    }
}

/// Two-bar pause — mirrors the existing web waiting SVG
/// (`viewBox="0 0 16 16"`, rects 2.5×10 at x=4 and x=9.5).
private struct WaitingIcon: View {
    let size: CGFloat

    private static let barWidthRatio: CGFloat = 2.5 / 16
    private static let barHeightRatio: CGFloat = 10 / 16
    private static let gapRatio: CGFloat = 3 / 16   // 9.5 - 4 - 2.5 = 3
    private static let cornerRatio: CGFloat = 1 / 16

    var body: some View {
        let barWidth = size * Self.barWidthRatio
        let barHeight = size * Self.barHeightRatio
        let gap = size * Self.gapRatio
        let corner = size * Self.cornerRatio
        HStack(spacing: gap) {
            RoundedRectangle(cornerRadius: corner)
                .fill(IrrColors.waiting)
                .frame(width: barWidth, height: barHeight)
            RoundedRectangle(cornerRadius: corner)
                .fill(IrrColors.waiting)
                .frame(width: barWidth, height: barHeight)
        }
        .frame(width: size, height: size)
    }
}
