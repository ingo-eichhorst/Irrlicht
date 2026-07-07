import AppKit

@MainActor
enum MenuBarImageBuilder {
    /// Which icon family the status item shows. Pure decision, extracted so
    /// the priority order is unit-testable without a SessionManager.
    enum IconState: Equatable {
        case attention // pending permission items — human must act
        case dots      // session-state circles
        case off       // no sessions, nothing pending
    }

    /// Pending consent outranks everything: while items are unanswered the
    /// daemon isn't monitoring those agents, so the bar must say "act to
    /// make me work again" — not show dots or the idle flame.
    static func iconState(pendingConsentCount: Int, sessionCount: Int) -> IconState {
        if pendingConsentCount > 0 { return .attention }
        if sessionCount > 0 { return .dots }
        return .off
    }

    static func build(
        sessionManager: SessionManager,
        gasTownProvider: GasTownProvider
    ) -> NSImage {
        switch iconState(
            pendingConsentCount: sessionManager.pendingWizardAgents.count,
            sessionCount: sessionManager.sessions.count
        ) {
        case .attention:
            // Full replacement — also suppresses the Gas Town badge so the
            // "do something" signal stays unambiguous; it returns once all
            // items are answered.
            return OffFlameImage.attention
        case .dots, .off:
            if let combined = combinedImage(sessionManager: sessionManager, gasTownProvider: gasTownProvider) {
                return combined
            }
            return OffFlameImage.menuBar
        }
    }

    private static func combinedImage(
        sessionManager: SessionManager,
        gasTownProvider: GasTownProvider
    ) -> NSImage? {
        let nonGtSessions = gasTownProvider.isDaemonRunning
            ? sessionManager.sessions.filter { !gasTownProvider.ownsSession($0) }
            : sessionManager.sessions

        // Issue #909: which content the icon shows is a user choice (default
        // .lights = today's behavior, unchanged for existing users). Either
        // half can come back nil (no sessions for .usage-only, or no
        // rate_limit data yet for .usage/.combined) without collapsing the
        // whole icon — composeSideBySide degrades to whichever half exists.
        let style = MenuBarStyle.current
        let dotsImage = style == .usage ? nil : MenuBarStatusRenderer.buildStatusImage(
            sessions: nonGtSessions,
            projectGroupOrder: sessionManager.projectGroupOrder
        )
        let quotaImage = style == .lights ? nil : QuotaMenuBarRenderer.imageForSelectedProvider(
            sessions: nonGtSessions,
            providerKey: MenuBarQuotaProvider.current
        )
        // Dots first (left), quota bars last (right) — closest to the
        // system status icons (WiFi/battery/clock), matching issue #909's
        // mockup ordering.
        let baseImage = composeSideBySide(dotsImage, quotaImage)

        guard gasTownProvider.isDaemonRunning else { return baseImage }

        let rigCount = sessionManager.apiGroups.first { $0.isGasTown }?.groups?.count ?? 0
        let emoji = NSAttributedString(string: "\u{26FD}", attributes: [
            .font: NSFont.systemFont(ofSize: 12)
        ])
        let countStr = NSAttributedString(string: "\(rigCount > 0 ? "\(rigCount)" : "")", attributes: [
            .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .bold),
            .foregroundColor: NSColor.white
        ])
        let badge = NSMutableAttributedString()
        badge.append(emoji)
        badge.append(countStr)

        return composeSideBySide(attributedStringImage(badge), baseImage)
    }

    /// Horizontally concatenates two optional images with a fixed gap,
    /// vertically centering each on the taller one's height. Either side
    /// may be nil — the other renders alone with no artificial gap. Shared
    /// by the quota+dots composition and the Gas Town badge+base
    /// composition, which both used to hand-roll this same NSImage math.
    static func composeSideBySide(_ left: NSImage?, _ right: NSImage?, gap: CGFloat = 4) -> NSImage? {
        switch (left, right) {
        case (nil, nil):
            return nil
        case (let l?, nil):
            return l
        case (nil, let r?):
            return r
        case (let l?, let r?):
            let totalWidth = l.size.width + gap + r.size.width
            let totalHeight = max(l.size.height, r.size.height)
            let combined = NSImage(size: NSSize(width: totalWidth, height: totalHeight))
            combined.lockFocus()
            l.draw(at: NSPoint(x: 0, y: (totalHeight - l.size.height) / 2),
                   from: .zero, operation: .sourceOver, fraction: 1)
            r.draw(at: NSPoint(x: l.size.width + gap, y: (totalHeight - r.size.height) / 2),
                   from: .zero, operation: .sourceOver, fraction: 1)
            combined.unlockFocus()
            combined.isTemplate = false
            return combined
        }
    }

    private static func attributedStringImage(_ text: NSAttributedString) -> NSImage {
        let size = text.size()
        let image = NSImage(size: size)
        image.lockFocus()
        text.draw(at: .zero)
        image.unlockFocus()
        image.isTemplate = false
        return image
    }

}
