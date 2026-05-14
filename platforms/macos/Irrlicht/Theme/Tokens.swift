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

enum IrrColors {
    static let working   = Color(hex: IrrHex.working)
    static let waiting   = Color(hex: IrrHex.waiting)
    static let ready     = Color(hex: IrrHex.ready)
    static let cancelled = Color(hex: IrrHex.cancelled)

    // 12%-alpha soft backgrounds (--working-dim / --waiting-dim / --ready-dim).
    static let workingDim = working.opacity(0.12)
    static let waitingDim = waiting.opacity(0.12)
    static let readyDim   = ready.opacity(0.12)

    // Glow halos (--working-glow 0.25, --waiting-glow / --ready-glow 0.20).
    static let workingGlow = working.opacity(0.25)
    static let waitingGlow = waiting.opacity(0.20)
    static let readyGlow   = ready.opacity(0.20)

    static let pressureLow      = Color(hex: IrrHex.pressureLow)
    static let pressureMedium   = Color(hex: IrrHex.pressureMedium)
    static let pressureHigh     = Color(hex: IrrHex.pressureHigh)
    static let pressureCritical = Color(hex: IrrHex.pressureCritical)

    static let wsConnected    = Color(hex: IrrHex.wsConnected)
    static let wsConnecting   = Color(hex: IrrHex.wsConnecting)
    static let wsDisconnected = Color(hex: IrrHex.wsDisconnected)

    // Neutral surface fills derived from the system primary color so they
    // adapt to light/dark mode automatically. The macOS overlay keeps native
    // chrome (system window background, primary/secondary text) — brand
    // tokens are scoped to semantic surfaces (state dots, pressure, badges).
    static let surfaceHover = Color.primary.opacity(0.06)
    static let trackFill    = Color.primary.opacity(0.08)

    /// Pressure-scale color for a context-utilization percentage (0–100).
    /// Thresholds mirror the SessionRow.jsx `pressureClass()` helper in
    /// tools/irrlicht-design-system/ui_kits/dashboard/.
    static func pressure(utilization: Double) -> Color {
        switch utilization {
        case ..<50:  return pressureLow
        case ..<75:  return pressureMedium
        case ..<90:  return pressureHigh
        default:     return pressureCritical
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
