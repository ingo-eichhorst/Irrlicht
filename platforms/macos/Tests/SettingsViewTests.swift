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
        // Hosted through `PinnedSnapshotHost` rather than a bare
        // `NSHostingView`, and that is #1672 rather than tidiness. This is the
        // one test in the suite that renders `SettingsView`, so before the fix
        // it was the run that persisted `soundOnReady = funk` and
        // `soundOnContextPressure = sosumi` into the developer's real
        // `com.apple.dt.xctest.tool` domain. The row no longer writes on
        // render, but this render is also the only thing driving
        // `reconcileNotificationsMasterDefault()` and the daemon reconciles at
        // SettingsView.swift:413-452, each of which writes an `@AppStorage`
        // key — so the domain the writes CAN reach is pinned here too, instead
        // of the fix resting on one call site staying quiet.
        //
        // The host also pins dark aqua (its default), which is what this test
        // set by hand: `NSColor.windowBackgroundColor` then resolves
        // deterministically. The test verifies opacity, not hue, but
        // appearance-pinning keeps the render stable across themes.
        //
        // SettingsView requires its environment objects (crashes without
        // them). startingUpdater: false keeps Sparkle from starting its
        // update cycle, which would fail outside an app bundle.
        let store = InMemoryDefaults()
        let view = SettingsView(isPresented: .constant(true),
                                showPermissionsReview: .constant(false),
                                sessionManager: SessionManager(defaults: store))
            .environmentObject(UpdateManager(startingUpdater: false))
            .environmentObject(DaemonManager())
        // SettingsView pins its width to SessionListView.panelWidth (issue
        // #940 — shared with History/the session list) and its own 520 height.
        let hosting = PinnedSnapshotHost(view,
                                         width: SessionListView.panelWidth,
                                         height: 520,
                                         defaults: store).view

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
