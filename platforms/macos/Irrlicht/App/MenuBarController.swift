import AppKit
import Combine
import SwiftUI

/// Owns the menu-bar status item and the popover NSPanel that hosts
/// `SessionListView`. Replaces SwiftUI's `MenuBarExtra(.window)` so we
/// control content-size changes + panel-resize + re-anchor as one atomic
/// frame change, eliminating the one-frame flash on group collapse.
///
/// Sizing strategy:
/// - `NSHostingController` auto-syncs its `preferredContentSize` to the
///   hosting SwiftUI view's measurement. Setting it as the panel's
///   `contentViewController` propagates that into the window's content
///   size, which triggers `windowDidResize(_:)` — the one place we
///   re-anchor the panel top under the status item.
/// - Re-anchoring uses `setFrame(_:display:animate:)` with animate=false
///   so content change + size change + origin change land in one pass.
@MainActor
final class MenuBarController: NSObject {
    private let daemonManager: DaemonManager
    private let sessionManager: SessionManager
    private let gasTownProvider: GasTownProvider

    private let statusItem: NSStatusItem
    private let panel: NSPanel
    private let hostingController: NSHostingController<AnyView>

    private var imageSubscription: AnyCancellable?
    private var globalMonitor: Any?
    private var escapeMonitor: Any?
    private var resignObserver: NSObjectProtocol?
    private var anchoring = false  // guards re-entrancy from setFrame during windowDidResize

    init(
        daemonManager: DaemonManager,
        sessionManager: SessionManager,
        gasTownProvider: GasTownProvider
    ) {
        self.daemonManager = daemonManager
        self.sessionManager = sessionManager
        self.gasTownProvider = gasTownProvider
        self.statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        let root = SessionListView()
            .environmentObject(daemonManager)
            .environmentObject(sessionManager)
            .environmentObject(gasTownProvider)
        self.hostingController = NSHostingController(rootView: AnyView(root))

        self.panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: SessionListView.panelWidth, height: 200),
            styleMask: [.borderless, .nonactivatingPanel, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        super.init()

        configurePanel()
        configureStatusItem()
        subscribeToStateChanges()
    }

    deinit {
        if let globalMonitor { NSEvent.removeMonitor(globalMonitor) }
        if let escapeMonitor { NSEvent.removeMonitor(escapeMonitor) }
        if let resignObserver { NotificationCenter.default.removeObserver(resignObserver) }
    }

    // MARK: - Setup

    private func configurePanel() {
        panel.level = .popUpMenu
        panel.hidesOnDeactivate = false
        panel.hasShadow = true
        panel.isMovable = false
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.becomesKeyOnlyIfNeeded = true
        panel.animationBehavior = .none
        panel.isReleasedWhenClosed = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        panel.delegate = self

        panel.contentViewController = hostingController

        // Rounded corners to match the previous MenuBarExtra(.window) look.
        // Panel is already transparent (isOpaque = false, backgroundColor =
        // .clear), so clipping the hosting view's layer gives rounded
        // corners and `hasShadow = true` follows the rounded silhouette.
        hostingController.view.wantsLayer = true
        if let layer = hostingController.view.layer {
            layer.cornerRadius = 10
            layer.cornerCurve = .continuous
            layer.masksToBounds = true
        }
    }

    private func configureStatusItem() {
        guard let button = statusItem.button else { return }
        button.target = self
        button.action = #selector(togglePanel)
        button.sendAction(on: [.leftMouseDown, .rightMouseDown])
        button.setAccessibilityLabel("Irrlicht")
        button.setAccessibilityRole(.menuButton)
    }

    private func subscribeToStateChanges() {
        let sessionSignal = sessionManager.objectWillChange
            .map { _ in () }
            .eraseToAnyPublisher()
        let gasTownSignal = gasTownProvider.objectWillChange
            .map { _ in () }
            .eraseToAnyPublisher()

        imageSubscription = sessionSignal
            .merge(with: gasTownSignal)
            .debounce(for: .milliseconds(50), scheduler: RunLoop.main)
            .sink { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.rebuildStatusImage()
                }
            }
        rebuildStatusImage()
    }

    private func rebuildStatusImage() {
        statusItem.button?.image = MenuBarImageBuilder.build(
            sessionManager: sessionManager,
            gasTownProvider: gasTownProvider
        )
    }

    // MARK: - Show / hide

    @objc private func togglePanel() {
        if panel.isVisible {
            hidePanel()
        } else {
            showPanel()
        }
    }

    private func showPanel() {
        // Force the hosting controller to lay out so preferredContentSize
        // is measured before we position the panel.
        hostingController.view.layoutSubtreeIfNeeded()
        anchorPanelToStatusItem()
        panel.orderFrontRegardless()
        installDismissMonitors()
    }

    private func hidePanel() {
        panel.orderOut(nil)
        removeDismissMonitors()
    }

    /// Place the panel with its top edge just below the status item and
    /// its left edge aligned with the button, opening rightward. If the
    /// panel would clip the right edge of the screen, the clamp below
    /// shifts it left so the right edge sits inside `visibleFrame`.
    /// Uses the CURRENT panel size — the window's content size is
    /// already synced by NSHostingController before this is called.
    private func anchorPanelToStatusItem() {
        guard !anchoring else { return }
        anchoring = true
        defer { anchoring = false }

        let panelSize = panel.frame.size
        var origin = statusItemOrigin(panelSize: panelSize) ?? fallbackOrigin(panelSize: panelSize)

        let screen = NSScreen.screens.first(where: { $0.frame.contains(origin) })
            ?? NSScreen.main
            ?? NSScreen.screens.first
        if let visible = screen?.visibleFrame {
            if origin.x + panelSize.width > visible.maxX {
                origin.x = visible.maxX - panelSize.width - 8
            }
            if origin.x < visible.minX {
                origin.x = visible.minX + 8
            }
            if origin.y < visible.minY {
                origin.y = visible.minY + 8
            }
        }

        panel.setFrameOrigin(origin)
    }

    /// Compute the panel origin anchored to the status item button.
    /// Returns nil when the button isn't hosted in a window (hidden
    /// from the menu bar), or when its on-screen rect doesn't
    /// intersect any screen — both cases mean the icon is effectively
    /// invisible (e.g. swallowed by the notch) and the caller should
    /// fall back to the primary screen's top-right.
    private func statusItemOrigin(panelSize: NSSize) -> NSPoint? {
        guard let button = statusItem.button, let window = button.window else { return nil }
        let buttonRectInWindow = button.convert(button.bounds, to: nil)
        let buttonRectOnScreen = window.convertToScreen(buttonRectInWindow)
        let onAScreen = NSScreen.screens.contains { $0.frame.intersects(buttonRectOnScreen) }
        guard onAScreen else { return nil }
        return NSPoint(
            x: buttonRectOnScreen.minX,
            y: buttonRectOnScreen.minY - panelSize.height - 2
        )
    }

    private func fallbackOrigin(panelSize: NSSize) -> NSPoint {
        let screen = NSScreen.main ?? NSScreen.screens.first
        guard let visible = screen?.visibleFrame else { return .zero }
        return NSPoint(
            x: visible.maxX - panelSize.width - 8,
            y: visible.maxY - panelSize.height - 2
        )
    }

    // MARK: - Dismiss handling

    private func installDismissMonitors() {
        // Both NSEvent global monitors and NotificationCenter observers
        // with queue=.main deliver on the main thread — assumeIsolated
        // is the cheap path; Task { @MainActor } would add an
        // unnecessary hop.
        if globalMonitor == nil {
            globalMonitor = NSEvent.addGlobalMonitorForEvents(
                matching: [.leftMouseDown, .rightMouseDown]
            ) { [weak self] _ in
                MainActor.assumeIsolated { self?.hidePanel() }
            }
        }
        if escapeMonitor == nil {
            escapeMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown]) { [weak self] event in
                guard event.keyCode == 53 else { return event }  // 53 = Escape
                MainActor.assumeIsolated { self?.hidePanel() }
                return nil
            }
        }
        if resignObserver == nil {
            resignObserver = NotificationCenter.default.addObserver(
                forName: NSApplication.didResignActiveNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated { self?.hidePanel() }
            }
        }
    }

    private func removeDismissMonitors() {
        if let globalMonitor {
            NSEvent.removeMonitor(globalMonitor)
            self.globalMonitor = nil
        }
        if let escapeMonitor {
            NSEvent.removeMonitor(escapeMonitor)
            self.escapeMonitor = nil
        }
        if let resignObserver {
            NotificationCenter.default.removeObserver(resignObserver)
            self.resignObserver = nil
        }
    }
}

// MARK: - NSWindowDelegate

extension MenuBarController: NSWindowDelegate {
    /// Fires after `NSHostingController` propagates its preferred content
    /// size into the panel. Re-anchor so the top edge stays pinned to the
    /// status item regardless of which direction the size changed.
    /// `NSWindow` delegate callbacks dispatch on the main thread, so
    /// `assumeIsolated` is safe and skips the Task hop.
    nonisolated func windowDidResize(_ notification: Notification) {
        MainActor.assumeIsolated {
            guard (notification.object as? NSWindow) === self.panel, self.panel.isVisible else { return }
            self.anchorPanelToStatusItem()
        }
    }
}
