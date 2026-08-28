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
        // SettingsView pins its width to SessionListView.panelWidth (issue
        // #940 — shared with History/the session list) and its own 520 height.
        let hosting = hostedSettingsPanel(height: 520, defaults: InMemoryDefaults())

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


    // MARK: - #1854: the notifications section must not eat the panel margin

    /// The half-point of slack every geometry assertion below carries.
    ///
    /// Layout lands on integral points on this machine, so this absorbs
    /// rounding on a future OS rather than weakening a bound. Stated once
    /// because several of these assertions land exactly ON their bound — the
    /// row saturates its box by construction, which is the whole subject.
    private static let layoutTolerance: CGFloat = 0.5

    /// The view class-name fragments the walk below treats as "a control whose
    /// position is the row's position".
    ///
    /// These are SwiftUI's own private class names, so the list is the part of
    /// this helper most likely to rot. When it does, the walk finds nothing and
    /// the guards in each test say so by name rather than reporting a
    /// comfortable pass — which is why those guards assert what was found, not
    /// merely how much.
    private static let measurableControlNames = [
        "PopupButton", "TextField", "SegmentedControl", "FocusRing",
    ]

    /// Every AppKit-backed control in a laid-out hierarchy, with its frame in
    /// `root`'s coordinate space.
    ///
    /// SwiftUI draws `Text`, `Capsule` and friends into layers, but a `Picker`,
    /// a `TextField`, a `Button` and a segmented control are each backed by a
    /// real `NSView`. Those are what this reads, so "where did the row actually
    /// land" is answered by the rendered hierarchy rather than by adding up
    /// modifier constants — a sum would just restate the code under test.
    ///
    /// The blind spot worth knowing: a `Text` is NOT here, so this cannot see
    /// the label truncating. `testTheWidestRowFitsTheContentBoxUntruncated`
    /// covers the width at which truncation would start.
    private func controlFrames(in root: NSView) -> [(name: String, frame: CGRect)] {
        var found: [(name: String, frame: CGRect)] = []
        func walk(_ view: NSView) {
            let name = String(describing: type(of: view))
            if Self.measurableControlNames.contains(where: name.contains) {
                found.append((name: name, frame: view.convert(view.bounds, to: root)))
            }
            view.subviews.forEach(walk)
        }
        walk(root)
        return found
    }

    /// The whole Settings panel, laid out at `panelWidth` and the given height.
    ///
    /// One factory rather than a copy per test: the argument list of
    /// `SettingsView.init` and the two environment objects it crashes without
    /// are exactly the kind of thing two copies stop agreeing about.
    private func hostedSettingsPanel(height: CGFloat,
                                     defaults store: InMemoryDefaults) -> NSView {
        let view = SettingsView(isPresented: .constant(true),
                                showPermissionsReview: .constant(false),
                                sessionManager: SessionManager(defaults: store))
            .environmentObject(UpdateManager(startingUpdater: false))
            .environmentObject(DaemonManager())
        return PinnedSnapshotHost(view,
                                  width: SessionListView.panelWidth,
                                  height: height,
                                  defaults: store).view
    }

    /// The width a `NotificationEventRow` asks for when nothing constrains it.
    ///
    /// Hosted through `PinnedSnapshotHost`, not a bare `NSHostingView`, and
    /// that is #1672 rather than tidiness: the row carries an `@AppStorage`
    /// sound choice, so an unpinned host resolves the developer's real
    /// `com.apple.dt.xctest.tool` domain. A `.speak(voice)` value sitting
    /// there makes the row render a SECOND line, and this measurement would
    /// then be taken over a different subtree than the one it reports on.
    private func idealWidth(ofRowFor event: NotificationEvent) -> CGFloat {
        let row = NotificationEventRow(event: event,
                                       enabled: .constant(true),
                                       sampleText: "",
                                       onImportError: { _ in })
            .toggleStyle(IrrlichtSwitchToggleStyle())
        // Wide enough that nothing constrains the row, so `fittingSize`
        // reports what it asks for rather than what it was given.
        return PinnedSnapshotHost(row, width: 1000, height: 200,
                                  defaults: InMemoryDefaults()).view.fittingSize.width
    }

    /// The width a legacy scroller ("Show scroll bars: Always") takes off the
    /// scroll content, asked of AppKit rather than typed: the constant is 15 on
    /// some releases and 17 on others, and a literal that stopped matching would
    /// silently relax the very margin this test is defending.
    private var legacyScrollerWidth: CGFloat {
        NSScroller.scrollerWidth(for: .regular, scrollerStyle: .legacy)
    }

    /// #1854 — the defect, measured on the rows that carry it.
    ///
    /// `SettingsView` puts `.padding(.horizontal, IrrSpacing.sp4)` on its scroll
    /// content inside a panel pinned to `SessionListView.panelWidth`, so a row
    /// gets `380 - 2 × 16 = 348` pt. Before the fix the notification rows were
    /// built entirely from parts that cannot shrink, so their minimum width was
    /// a constant that saturated that box — see
    /// `LeadingToggle.compressibleLabel` for the mechanism, and
    /// `testTheWidestRowFitsTheContentBoxUntruncated` for the measurement.
    ///
    /// A legacy scroller is the everyday thing that takes those points away.
    /// This hosts the rows in the box they really get in that case —
    /// `panelWidth` minus the scroller, minus the same horizontal padding — and
    /// asserts each one stays inside its own padding.
    ///
    /// Every event is rendered, not just the widest, because two of the three
    /// overflowed before the fix. Pinning only the widest would let a future
    /// label edit push either of the others over while this stayed green.
    ///
    /// `ContextThresholdRow`, the fourth child of the production stack, is
    /// deliberately not here: it ends in a `Spacer()` and so is compressible by
    /// construction, and it renders 117pt clear of the trailing margin. The
    /// three rows below are the ones whose width is a constant.
    func testNotificationSectionStaysInsideAScrollerNarrowedContentBox() throws {
        let boxWidth = SessionListView.panelWidth - legacyScrollerWidth
        let inset = IrrSpacing.sp4
        let slack = Self.layoutTolerance

        let section = VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            ForEach(NotificationEvent.allCases, id: \.self) { event in
                NotificationEventRow(
                    event: event,
                    enabled: .constant(true),
                    // Speech text only — handed to SoundPlayer.preview, it
                    // contributes nothing to layout. The row's width comes
                    // from `event.displayName`.
                    sampleText: "",
                    onImportError: { _ in }
                )
            }
        }
        .padding(.horizontal, inset)
        .frame(width: boxWidth)
        // SettingsView applies this to the whole panel; the rows' toggles are
        // drawn by it, so the measurement needs it too.
        .toggleStyle(IrrlichtSwitchToggleStyle())

        let hosting = PinnedSnapshotHost(section, width: boxWidth, height: 160,
                                         defaults: InMemoryDefaults()).view
        let controls = controlFrames(in: hosting)
        let names = controls.map(\.name)

        // Fail loudly when the measurement could not be taken at all: an empty
        // walk reports "nothing outside the margin" exactly like a healthy
        // section would, and that is the one answer this test must never give
        // by accident (AGENTS.md — a verification mechanism must fail loudly
        // when it cannot run). Assert WHAT was found, not just how much: a
        // count alone is met by two pickers while a row is missing entirely.
        for fragment in ["PopupButton", "FocusRing"] {
            XCTAssertTrue(
                names.contains { $0.contains(fragment) },
                "no \(fragment) in the hosted section — the walk found \(names). SwiftUI's private class names may have changed; see measurableControlNames."
            )
        }
        XCTAssertEqual(
            names.filter { $0.contains("PopupButton") }.count, NotificationEvent.allCases.count,
            "expected one sound picker per event (\(NotificationEvent.allCases.count)), walked \(names) — the section did not render every row, so a per-row margin assertion would skip one"
        )

        for (name, frame) in controls {
            XCTAssertGreaterThanOrEqual(
                frame.minX, inset - slack,
                "#1854: \(name) starts at x=\(frame.minX) in a \(boxWidth)pt box, inside the \(inset)pt margin."
            )
            XCTAssertLessThanOrEqual(
                frame.maxX, boxWidth - inset + slack,
                "#1854: \(name) ends at x=\(frame.maxX) in a \(boxWidth)pt box, past the \(boxWidth - inset)pt margin."
            )
        }
    }

    /// The widest notification row must fit the panel's padded content box
    /// without truncating — the cost the fix trades for a correct margin, and
    /// the one thing `controlFrames` cannot see (a `Text` is not an `NSView`).
    ///
    /// Deliberately `<=`, not `==`. The row measuring *exactly* the box is the
    /// accident that caused #1854, and freezing it as a contract would turn the
    /// obvious future improvement — giving the row real slack by narrowing the
    /// 112pt picker or shortening a label — into a red test. `<=` is the
    /// property the panel actually needs.
    ///
    /// The measured widths are printed into the failure message rather than
    /// typed into a comment, so the "348pt" figure this PR quotes stays derived
    /// (AGENTS.md — a figure that documents behaviour states what produces it).
    func testTheWidestRowFitsTheContentBoxUntruncated() throws {
        let box = SessionListView.panelWidth - 2 * IrrSpacing.sp4
        let widths = NotificationEvent.allCases.map { (event: $0, width: idealWidth(ofRowFor: $0)) }
        let widest = try XCTUnwrap(widths.max(by: { $0.width < $1.width }),
                                   "NotificationEvent.allCases is empty — nothing was measured")
        let measured = widths.map { "\($0.event) \($0.width)" }.joined(separator: ", ")

        XCTAssertLessThanOrEqual(
            widest.width, box + Self.layoutTolerance,
            "the widest notification row asks for \(widest.width)pt against a \(box)pt content box (measured: \(measured)) — its label truncates at the panel's own width."
        )
    }

    /// #1854 — the acceptance criterion, stated on the whole panel.
    ///
    /// LOCK, not a red-first defect test: at `SessionListView.panelWidth` with
    /// the overlay scrollers a test process gets, the widest notification row
    /// fits its box, so this comparison passes on `main` too. It is here to pin
    /// the criterion the issue asks for — the horizontal inset of every settings
    /// row is identical with notifications off and on — so a future change that
    /// reintroduces a sideways shift at the panel's own width is caught.
    /// `testNotificationSectionStaysInsideAScrollerNarrowedContentBox` is the
    /// one that was seen red.
    func testPanelHorizontalInsetIsIdenticalWithNotificationsOffAndOn() throws {
        // Named `panelControlFrames`, not an overload of `controlFrames`: a
        // local function of the same name shadows the member, so the body
        // would resolve its own call recursively.
        func panelControlFrames(notificationsEnabled: Bool) -> [CGRect] {
            let store = InMemoryDefaults()
            store.set(notificationsEnabled, forKey: NotificationSettings.masterEnabledKey)
            // 603pt is the panel's own measured height (header + the 520pt
            // scroll cap + footer), so the content fills the host exactly and
            // nothing is centred or clipped by the frame itself.
            return controlFrames(in: hostedSettingsPanel(height: 603, defaults: store))
                .map(\.frame)
        }

        let collapsed = panelControlFrames(notificationsEnabled: false)
        let expanded = panelControlFrames(notificationsEnabled: true)

        // Same "could not look" guard as above, on both renders.
        for (label, frames) in [("collapsed", collapsed), ("expanded", expanded)] {
            XCTAssertGreaterThanOrEqual(
                frames.count, 3,
                "\(label) panel yielded \(frames.count) controls — the hierarchy walk cannot have worked"
            )
        }
        // And that the two renders are genuinely different states, so an
        // `@AppStorage` seed that silently failed to reach the view cannot make
        // this pass by comparing a render with itself.
        XCTAssertGreaterThan(
            expanded.count, collapsed.count,
            "enabling notifications added no controls (\(collapsed.count) -> \(expanded.count)) — the section did not expand, so this comparison is vacuous"
        )

        let collapsedLeading = try XCTUnwrap(collapsed.map(\.minX).min())
        let expandedLeading = try XCTUnwrap(expanded.map(\.minX).min())
        let expandedTrailing = try XCTUnwrap(expanded.map(\.maxX).max())

        XCTAssertEqual(
            collapsedLeading, expandedLeading, accuracy: Self.layoutTolerance,
            "#1854: the leading inset moved from \(collapsedLeading) to \(expandedLeading) when notifications were enabled. It must not change."
        )
        XCTAssertEqual(
            collapsedLeading, IrrSpacing.sp4, accuracy: Self.layoutTolerance,
            "#1854: the leading inset is \(collapsedLeading), not the \(IrrSpacing.sp4)pt the scroll content's padding asks for."
        )
        XCTAssertLessThanOrEqual(
            expandedTrailing, SessionListView.panelWidth - IrrSpacing.sp4 + Self.layoutTolerance,
            "#1854: the expanded section reaches x=\(expandedTrailing), past the \(SessionListView.panelWidth - IrrSpacing.sp4)pt trailing margin."
        )
    }
}
