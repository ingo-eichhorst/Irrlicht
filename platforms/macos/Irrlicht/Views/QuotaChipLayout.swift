import CoreGraphics

/// How tightly the header packs its quota chips. One per provider-count band,
/// decided by `QuotaChipLayout.forChipCount(_:)` and mirrored by
/// `quotaChipLayout` in `platforms/web/quotaChips.js` (#2063).
///
/// `regular` and `compact` are the two layouts the header had before #2063 and
/// keep its exact numbers (locked by `QuotaChipLayoutTests`). `tight` and
/// `dense` trade fixed bar widths for flexible ones so three to five chips
/// share the 380pt header instead of overflowing.
enum QuotaChipDensity: String, Equatable {
    /// One chip: fixed 70pt bars and the inline reset label.
    case regular
    /// Two chips: fixed 60pt bars, reset label dropped (lives in the tooltip).
    case compact
    /// Three chips: bars flex between `minBarWidth` and 60pt; labels and
    /// percent stay.
    case tight
    /// Four or five chips: flexible bars, and the `5h`/`7d` window labels
    /// drop too — the chip tooltip still names each window.
    case dense

    /// Fixed bar width, or nil when the bar flexes between `minBarWidth` and
    /// `maxBarWidth`.
    var fixedBarWidth: CGFloat? {
        switch self {
        case .regular: return 70
        case .compact: return 60
        case .tight, .dense: return nil
        }
    }

    /// The narrowest a flexible bar gets before the header runs out of room.
    var minBarWidth: CGFloat {
        switch self {
        case .regular, .compact: return fixedBarWidth ?? 0
        case .tight: return 24
        case .dense: return 6
        }
    }

    /// A flexible bar never grows past the compact tier's fixed width, so a
    /// roomy header renders the same bars it always did.
    static let maxBarWidth: CGFloat = 60

    var showsWindowLabel: Bool { self != .dense }
    var showsResetLabel: Bool { self == .regular }

    /// Width of the percent label. 28 before #2063; 23 in the flexible tiers,
    /// which holds "100%" — measured at 22.25pt for 9pt medium monospaced with
    /// `NSFont.monospacedSystemFont(ofSize: 9, weight: .medium)` and
    /// `NSString.size(withAttributes:)` on the development Mac.
    var percentWidth: CGFloat {
        switch self {
        case .regular, .compact: return 28
        case .tight, .dense: return 23
        }
    }

    /// Spacing between a row's label, bar and percent.
    var rowSpacing: CGFloat {
        switch self {
        case .regular, .compact: return 6
        case .tight, .dense: return 3
        }
    }

    /// Spacing between the provider icon and the chip body.
    var iconSpacing: CGFloat {
        switch self {
        case .regular, .compact: return 6
        case .tight, .dense: return 4
        }
    }

    /// Spacing between adjacent chips in the header.
    var chipSpacing: CGFloat {
        switch self {
        case .regular, .compact: return 8
        case .tight: return 6
        case .dense: return 4
        }
    }

    /// The usage-mode body's minimum width. It matches a subscription row's
    /// width in the fixed tiers; the flexible tiers drop it so the headline
    /// shares the header like the bars do.
    var usageMinWidth: CGFloat? {
        switch self {
        case .regular, .compact: return 88
        case .tight, .dense: return nil
        }
    }

    /// The dense tier shows the usage headline only, without the "spend" line.
    var usageShowsSublabel: Bool { self != .dense }
}

/// The header's visible/overflow split plus the density every visible chip
/// renders at — a pure function of the provider count, so a test can assert it
/// for each count rather than inferring it from pixels.
struct QuotaChipLayout: Equatable {
    let visibleCount: Int
    let overflowCount: Int
    let density: QuotaChipDensity

    /// Up to this many providers render inline with no overflow pill.
    static let maxInline = 5

    /// Chips shown beside the "+N more" pill once there are more than
    /// `maxInline` providers. One fewer than `maxInline`, because five dense
    /// chips plus the ~56pt pill do not fit the header's chip budget (see
    /// `QuotaChipLayoutTests.testTheWorstCaseRowFitsTheHeaderBudget`).
    static let maxBesideOverflow = 4

    static func forChipCount(_ count: Int) -> QuotaChipLayout {
        let n = max(0, count)
        let visible = n > maxInline ? maxBesideOverflow : n
        return QuotaChipLayout(visibleCount: visible,
                               overflowCount: n - visible,
                               density: density(forChipCount: n))
    }

    private static func density(forChipCount n: Int) -> QuotaChipDensity {
        switch n {
        case ...1: return .regular
        case 2: return .compact
        case 3: return .tight
        default: return .dense
        }
    }
}
