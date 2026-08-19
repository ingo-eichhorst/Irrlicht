import AppKit
import XCTest
@testable import Irrlicht

/// Locks the property #1530's blocker 1 is about: the snapshot rasteriser
/// honours the scale it is ASKED for, not the scale the machine happens to
/// have.
///
/// The obvious test — "renders at 2×" — is the trap this repo has already paid
/// for twice (#1034, #1044, recorded in `AGENTS.md`): on a Retina Mac it
/// passes whether the code pins a scale or inherits the screen's, so it is
/// green for the wrong reason on every machine anyone would run it on. The
/// fix is to drive several scales through one view. Whatever
/// `NSScreen.main.backingScaleFactor` is, at most one arm can agree with it,
/// so an implementation that inherits the screen fails the others — on a 1×
/// runner and a 2× laptop alike.
@MainActor
final class PinnedScaleSnapshotTests: XCTestCase {

    /// A plain, cheap view: this measures the rasteriser's geometry, not any
    /// particular UI.
    private func subject() -> NSView {
        let view = NSView(frame: CGRect(x: 0, y: 0, width: 350, height: 48))
        view.wantsLayer = true
        view.layer?.backgroundColor = NSColor.systemTeal.cgColor
        return view
    }

    func testRasterizerHonoursTheRequestedScaleAndNotTheScreens() {
        let points = CGSize(width: 350, height: 48)
        for scale in [CGFloat(1), 2, 3] {
            let image = PinnedScaleSnapshot.rasterize(subject(), scale: scale)
            guard let rep = image.representations.first as? NSBitmapImageRep else {
                return XCTFail("expected a bitmap representation at \(scale)×")
            }
            XCTAssertEqual(rep.pixelsWide, Int(points.width * scale),
                           "width at \(scale)× (screen is \(Self.screenScale)×)")
            XCTAssertEqual(rep.pixelsHigh, Int(points.height * scale),
                           "height at \(scale)× (screen is \(Self.screenScale)×)")
            // Point size is the invariant across all of them — the geometry
            // does not change, only how finely it is sampled.
            XCTAssertEqual(image.size, points, "point size at \(scale)×")
        }
    }

    /// The default the six snapshot suites use is the scale the committed
    /// references were recorded at. Read off the PNGs on disk rather than
    /// restated, so a re-record at another scale fails here instead of
    /// silently redefining "reference scale".
    func testDefaultScaleMatchesTheCommittedReferences() {
        let reference = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appendingPathComponent("__Snapshots__/SessionRowSnapshotTests/testRelayCloudOnline.1.png")
        guard let data = try? Data(contentsOf: reference),
              let rep = NSBitmapImageRep(data: data) else {
            return XCTFail("could not read \(reference.path)")
        }
        // `rep.size` is the PNG's point size (it carries 144 dpi);
        // `pixelsWide` is its pixel size. Their ratio is the scale it was
        // recorded at.
        XCTAssertGreaterThan(rep.size.width, 0)
        XCTAssertEqual(CGFloat(rep.pixelsWide) / rep.size.width,
                       PinnedScaleSnapshot.referenceScale,
                       "committed references are \(rep.pixelsWide)px wide for "
                       + "\(rep.size.width)pt — PinnedScaleSnapshot.referenceScale disagrees")
    }

    /// Reported on every run so a runner's log carries the measured premise of
    /// the whole fix rather than an inference about virtual displays.
    func testReportTheHostsBackingScale() {
        print("host backing scale: \(Self.screenScale)× "
              + "(NSScreen.main: \(NSScreen.main.map { "\($0.backingScaleFactor)" } ?? "none"))")
    }

    private static var screenScale: CGFloat { NSScreen.main?.backingScaleFactor ?? 0 }
}
