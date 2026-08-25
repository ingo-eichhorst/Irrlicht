import AppKit
import SwiftUI

// Tokens transcribed from tools/irrlicht-design-system/colors_and_type.css.
// Single source of truth for brand-aligned styling across the overlay.
// When values here change, update colors_and_type.css too — and vice versa.

enum IrrHex {
    // The Light System
    static let working   = "#8B5CF6"
    static let waiting   = "#FF9500"
    static let ready     = "#34C759"
    static let cancelled = "#8E8E93"
    // Unrecognized session state (#1797) — a state this build has never heard
    // of, e.g. one written by a newer daemon. Same neutral grey as `cancelled`
    // by value, kept as its own token because the two mean different things:
    // `cancelled` is a state that was retired, `unknown` is one we can't read.
    static let unknown   = "#8E8E93"
    // Failed session state (#1802) — the session's own machinery failed: the
    // provider refused the call, credentials were rejected, the agent process
    // died mid-turn, or Irrlicht could not read the session.
    //
    // Value-equal to `pressureHigh` on purpose and by instruction (#1802): this
    // app already paints red at #FF3B30 — the pressure scale, the cache-bloat
    // pill, `OffFlameImage`'s attention badge — and a second, almost-identical
    // red would read as a rendering bug rather than as a distinction. It is
    // nonetheless its OWN token, for the reason `unknown` is separate from
    // `cancelled`: the two mean different things, and a future retune of the
    // context-pressure scale must not silently restyle the error state.
    static let error     = "#FF3B30"

    // Pressure scale
    static let pressureLow      = "#34C759"
    static let pressureMedium   = "#FF9500"
    static let pressureHigh     = "#FF3B30"
    static let pressureCritical = "#D70015"

    // Connection state
    static let wsConnected    = "#34C759"
    static let wsConnecting   = "#FF9500"
    static let wsDisconnected = "#FF3B30"
}

/// Bare hex (no `#`) for inline SVG `fill="#..."` markup. Mirrors `IrrHex`
/// so the two formats stay paired; consumers that need a leading `#` use
/// `IrrHex.*` directly with `Color(hex:)`.
enum IrrSVG {
    static let working   = "8B5CF6"
    static let waiting   = "FF9500"
    static let ready     = "34C759"
    static let cancelled = "8E8E93"
    static let unknown   = "8E8E93"
    static let error     = "FF3B30"
}

enum IrrColors {
    static let working   = Color(hex: IrrHex.working)
    static let waiting   = Color(hex: IrrHex.waiting)
    static let ready     = Color(hex: IrrHex.ready)
    static let cancelled = Color(hex: IrrHex.cancelled)
    static let unknown   = Color(hex: IrrHex.unknown)
    static let error     = Color(hex: IrrHex.error)

    // 12%-alpha soft backgrounds (--working-dim / --waiting-dim / --ready-dim).
    static let workingDim = working.opacity(0.12)
    static let waitingDim = waiting.opacity(0.12)
    static let readyDim   = ready.opacity(0.12)
    static let errorDim   = error.opacity(0.12)

    // Glow halos (--working-glow 0.25, --waiting-glow / --ready-glow 0.20).
    static let workingGlow = working.opacity(0.25)
    static let waitingGlow = waiting.opacity(0.20)
    static let readyGlow   = ready.opacity(0.20)

    static let pressureLow      = Color(hex: IrrHex.pressureLow)
    static let pressureMedium   = Color(hex: IrrHex.pressureMedium)
    static let pressureHigh     = Color(hex: IrrHex.pressureHigh)
    static let pressureCritical = Color(hex: IrrHex.pressureCritical)

    // Pill *text* color for the question box (issue #984). Text is drawn at
    // full opacity on a 12%-alpha wash of `waiting`, and `Color(hex:)` isn't
    // appearance-aware, so the fixed brand hue measured out at 2.03:1 against
    // the composited wash — well under WCAG AA's 4.5:1 for 9pt text — in one
    // or both appearances. This is a per-appearance re-tuning of the same hue
    // (not a new brand color) sized to clear 4.5:1 against the actual
    // measured wash color in each mode (light: #FDF3E7, dark: #372D21); the
    // wash itself keeps using `waiting` unchanged so glows/dots elsewhere are
    // untouched.
    static let waitingPillText = Color.adaptive(light: "#8F5300", dark: IrrHex.waiting)

    // Pill/alert *text* color for the session error line and the daemon-wide
    // error banner (#1802) — the same per-appearance retune `waitingPillText`
    // is, for the same reason and against the same 12% wash. `IrrHex.error`
    // itself measures under WCAG AA's 4.5:1 for 9pt text in BOTH appearances
    // once composited, so unlike `waitingPillText` neither mode gets to use
    // the plain brand hue.
    //
    // Every figure here is PRINTED BY THE CHECK, not typed beside it —
    // `swift test --filter TokenContrastTests` composites the wash and
    // computes the WCAG ratio from these very tokens:
    //
    //     raw IrrHex.error on its own 12% wash   light 3.02:1   dark 4.18:1
    //     errorPillText    on that same wash     light 5.30:1   dark 5.83:1
    //
    // The wash keeps using plain `error`, so dots, icons and glows elsewhere
    // are untouched.
    static let errorPillText = Color.adaptive(light: "#C1121C", dark: "#FF7A70")

    static let wsConnected    = Color(hex: IrrHex.wsConnected)
    static let wsConnecting   = Color(hex: IrrHex.wsConnecting)
    static let wsDisconnected = Color(hex: IrrHex.wsDisconnected)

    // Neutral surface fills derived from the system primary color so they
    // adapt to light/dark mode automatically. The macOS overlay keeps native
    // chrome (system window background, primary/secondary text) — brand
    // tokens are scoped to semantic surfaces (state dots, pressure, badges).
    static let surfaceHover       = Color.primary.opacity(0.06)
    // Subtler hover for nested rows (subagents) so parent vs. child
    // hierarchy stays legible.
    static let surfaceHoverSubtle = Color.primary.opacity(0.04)
    static let trackFill          = Color.primary.opacity(0.08)

    /// State/status string → color, mirroring the web `stateColor` palette
    /// (working/waiting/ready/error, muted fallback). Used for Gas Town
    /// global-agent dots, convoy progress, and rig status badges.
    ///
    /// `"error"` is here for the same reason the state enum has an `.error`
    /// case (#1802): without it a failing rig status fell through to
    /// `Color.secondary` — neutral grey, the "nothing to report" answer — at
    /// the one moment there is something to report. The states themselves come
    /// through `SessionState.State`, which is a compiler-forced switch; this
    /// map takes a raw String from a different payload and has to be kept in
    /// step by hand, which is what `IrrColorsForStateTests` pins.
    static func forState(_ s: String?) -> Color {
        switch s {
        case "working": return working
        case "waiting": return waiting
        case "ready":   return ready
        case "error":   return error
        default:        return Color.secondary
        }
    }
}

enum IrrSpacing {
    static let sp1: CGFloat = 4
    static let sp2: CGFloat = 8
    static let sp3: CGFloat = 12
    static let sp4: CGFloat = 16
    static let sp5: CGFloat = 24
    static let sp6: CGFloat = 32
}

enum IrrRadius {
    static let xs: CGFloat = 3
    static let sm: CGFloat = 4
    static let md: CGFloat = 6
    static let lg: CGFloat = 10
    static let xl: CGFloat = 16
}

enum IrrMotion {
    static let fast: Double = 0.2
    static let base: Double = 0.3
    static let slow: Double = 0.4

    /// Decelerate-settle ease-out matching --ease-out: cubic-bezier(0.16,1,0.3,1).
    static func easeOut(duration: Double = base) -> Animation {
        .timingCurve(0.16, 1, 0.3, 1, duration: duration)
    }
}

extension Color {
    /// Resolves to `light` in aqua appearance and `dark` in dark aqua —
    /// for tokens that need a genuinely different value per appearance
    /// (not just a fixed color that happens to sit on an adaptive system
    /// surface). `Color(hex:)` below is a single fixed color; this is the
    /// per-appearance counterpart.
    static func adaptive(light: String, dark: String) -> Color {
        Color(NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            return NSColor(Color(hex: isDark ? dark : light))
        })
    }

    /// Initialise from a hex string (`#RGB`, `#RRGGBB`, or `#AARRGGBB` — `#`
    /// optional). The token namespaces above are the canonical source; this
    /// initializer exists so they can be expressed as literal hex.
    init(hex: String) {
        let hex = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        var int: UInt64 = 0
        Scanner(string: hex).scanHexInt64(&int)
        let a: UInt64
        let r: UInt64
        let g: UInt64
        let b: UInt64
        switch hex.count {
        case 3:
            (a, r, g, b) = (255, (int >> 8) * 17, (int >> 4 & 0xF) * 17, (int & 0xF) * 17)
        case 6:
            (a, r, g, b) = (255, int >> 16, int >> 8 & 0xFF, int & 0xFF)
        case 8:
            (a, r, g, b) = (int >> 24, int >> 16 & 0xFF, int >> 8 & 0xFF, int & 0xFF)
        default:
            (a, r, g, b) = (1, 1, 1, 0)
        }

        self.init(
            .sRGB,
            red: Double(r) / 255,
            green: Double(g) / 255,
            blue: Double(b) / 255,
            opacity: Double(a) / 255
        )
    }
}
