import XCTest
import SwiftUI
@testable import Irrlicht

@MainActor
final class SettingsViewTests: XCTestCase {

    // Regression: SettingsView is hosted inside a transparent NSPanel.
    // MenuBarController configures the panel with isOpaque=false and
    // backgroundColor=.clear so the rounded-corner hosting-controller clip
    // works; the SwiftUI view itself must paint the solid background. If it
    // doesn't, the desktop wallpaper bleeds through the Settings overlay.
    //
    // This test renders SettingsView with NO outer background wrapper,
    // samples the four corners + center of the resulting bitmap, and asserts
    // every sampled pixel is fully opaque.
    func testSettingsViewBackgroundIsOpaque() throws {
        let view = SettingsView(isPresented: .constant(true))
        let hosting = NSHostingView(rootView: view)
        // Pin to dark aqua so NSColor.windowBackgroundColor resolves
        // deterministically — the test verifies opacity, not hue, but
        // appearance-pinning keeps the render stable across themes.
        hosting.appearance = NSAppearance(named: .darkAqua)
        // SettingsView hard-codes its own 320×380 frame.
        hosting.frame = CGRect(x: 0, y: 0, width: 320, height: 380)
        hosting.layoutSubtreeIfNeeded()

        guard let bitmap = hosting.bitmapImageRepForCachingDisplay(in: hosting.bounds) else {
            XCTFail("bitmapImageRepForCachingDisplay returned nil")
            return
        }
        hosting.cacheDisplay(in: hosting.bounds, to: bitmap)

        // Corners are the canary — the settings controls sit well inside the
        // padding, so the only thing that can paint the edge pixels is the
        // view's own background modifier. If the background is missing, these
        // sample points land on the transparent NSPanel layer.
        let w = bitmap.pixelsWide
        let h = bitmap.pixelsHigh
        let samples: [(String, Int, Int)] = [
            ("top-left",     2, 2),
            ("top-right",    w - 3, 2),
            ("bottom-left",  2, h - 3),
            ("bottom-right", w - 3, h - 3),
            ("center",       w / 2, h / 2),
        ]
        for (label, x, y) in samples {
            guard let color = bitmap.colorAt(x: x, y: y) else {
                XCTFail("colorAt(\(label) = \(x),\(y)) returned nil")
                continue
            }
            XCTAssertEqual(
                color.alphaComponent, 1.0, accuracy: 0.001,
                "SettingsView background must be fully opaque — \(label) alpha was \(color.alphaComponent). Regression of the transparent-panel bleedthrough bug."
            )
        }
    }
}
