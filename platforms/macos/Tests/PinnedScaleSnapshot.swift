import AppKit
import SnapshotTesting

/// A `.image` snapshot strategy that rasterises at a **pinned** backing scale
/// instead of the main screen's (#1530, blocker 1).
///
/// ## What was wrong
///
/// `SnapshotTesting`'s stock `Snapshotting<NSView, NSImage>.image` renders
/// through `-[NSView bitmapImageRepForCachingDisplay:]`, which for a
/// window-less view sizes its bitmap from the **main screen's** backing scale.
/// Every reference in `Tests/__Snapshots__` was recorded on a Retina Mac and
/// is therefore committed at 2× — 700×96 pixels at 144 dpi for a 350×48-point
/// row. A GitHub runner's virtual display is 1×, so the same view rasterises
/// to 350×48 pixels and 35 comparisons across six suites fail.
///
/// ## Scale, not text rasterisation — and how that was settled
///
/// The issue recorded these two as indistinguishable, because the failure text
///
///     Newly-taken snapshot@(350.0, 48.0) does not match reference@(350.0, 48.0).
///
/// reports POINT size, which is identical either way. But that string is not
/// ambiguous about which branch produced it. In
/// `SnapshotTesting/Snapshotting/NSImage.swift`, `compare(_:_:precision:perceptualPrecision:)`
/// emits the `@`-qualified form from exactly one place — the guard on
///
///     oldCgImage.width == newCgImage.width, oldCgImage.height == newCgImage.height
///
/// i.e. a **pixel-dimension** mismatch, which returns before a single byte is
/// compared. A rasterisation difference reaches the byte comparison below it
/// and produces the unqualified "Newly-taken snapshot does not match
/// reference." with no sizes at all. The runner printed the qualified form for
/// all 35, so all 35 are pixel-count mismatches. The point sizes print equal
/// because the reference PNG carries its 144 dpi, so `NSImage(data:)` reports
/// it as 350×48 points, and the fresh image is built as
/// `NSImage(size: view.bounds.size)`.
///
/// That is a **necessary** condition, not a sufficient one: it proves the
/// scale is wrong and says nothing about whether text also rasterises
/// differently, because a pixel-count mismatch masks every byte difference
/// behind it. `macos-swift.yml` reports the runner's measured backing scale so
/// the premise is a printed number rather than an inference.
///
/// ## Why this does not re-record anything
///
/// Every bitmap parameter is copied from the rep AppKit would have made
/// itself; only the pixel dimensions are overridden. On a 2× host the
/// reconstruction is parameter-for-parameter the same rep the stock strategy
/// builds, which is why all 32 committed references still pass byte-identically
/// — the check that this change alters no reference is the reference set
/// itself.
enum PinnedScaleSnapshot {
    /// The scale every committed reference was recorded at. Read off the PNGs:
    /// 700×96 pixels at 144 dpi for a 350×48-point view.
    static let referenceScale: CGFloat = 2

    /// Rasterise `view` at exactly `scale` device pixels per point.
    ///
    /// - Note: the stock strategy first runs `addImagesForRenderedViews`, which
    ///   substitutes bitmaps for `WKWebView`/`SCNView`/`SKView` subtrees that
    ///   cannot be cached synchronously. That helper is internal to
    ///   `SnapshotTesting` and every view snapshotted here is a plain
    ///   `NSHostingView` over SwiftUI, so it is omitted. A snapshot that ever
    ///   embeds one of those three types needs it back.
    static func rasterize(_ view: NSView, scale: CGFloat) -> NSImage {
        let bounds = view.bounds

        // The rep AppKit would have used, consulted only for its FORMAT.
        // Copying every parameter — rather than picking plausible ones — is
        // what keeps the output byte-identical to the stock strategy on a host
        // whose scale already matches.
        guard let format = view.bitmapImageRepForCachingDisplay(in: bounds) else {
            preconditionFailure("view is not renderable at \(bounds.size)")
        }
        guard let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: Int((bounds.width * scale).rounded()),
            pixelsHigh: Int((bounds.height * scale).rounded()),
            bitsPerSample: format.bitsPerSample,
            samplesPerPixel: format.samplesPerPixel,
            hasAlpha: format.hasAlpha,
            isPlanar: format.isPlanar,
            colorSpaceName: format.colorSpaceName,
            bitmapFormat: format.bitmapFormat,
            bytesPerRow: 0,
            bitsPerPixel: 0
        ) else {
            preconditionFailure("could not allocate a \(scale)× bitmap for \(bounds.size)")
        }

        // Point size < pixel size IS the scale: `cacheDisplay` reads the ratio
        // off the rep and transforms the context accordingly, which is what
        // makes this independent of any screen.
        rep.size = bounds.size
        view.cacheDisplay(in: bounds, to: rep)

        let image = NSImage(size: bounds.size)
        image.addRepresentation(rep)
        return image
    }
}

extension Snapshotting where Value == NSView, Format == NSImage {
    /// `.image`, rasterised at the scale the committed references were
    /// recorded at rather than at the host's. Use this and not `.image`.
    static var pinnedImage: Snapshotting {
        pinnedImage(scale: PinnedScaleSnapshot.referenceScale)
    }

    static func pinnedImage(scale: CGFloat) -> Snapshotting {
        Snapshotting<NSImage, NSImage>.image(precision: 1, perceptualPrecision: 1)
            .pullback { view in PinnedScaleSnapshot.rasterize(view, scale: scale) }
    }
}
