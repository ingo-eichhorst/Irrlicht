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

    /// True when `.usage` style has nothing to show (no renderable quota
    /// yet) but the dots view is actually renderable — see the fallback
    /// comment at its call site in `combinedImage`. Takes the already-built
    /// dots image rather than a raw session count: a non-zero session count
    /// doesn't guarantee `buildStatusImage` succeeds (e.g. sessions whose
    /// parent was pruned out from under them still carry a non-nil
    /// `parentSessionId` and get excluded from every project group), and
    /// checking the actual image avoids re-deriving that success/failure a
    /// second time. Pure decision, extracted for testability without a
    /// SessionManager, mirroring `iconState`.
    static func shouldFallBackToDotsForUsageStyle(
        style: MenuBarStyle,
        quotaImage: NSImage?,
        dotsImage: NSImage?
    ) -> Bool {
        style == .usage && quotaImage == nil && dotsImage != nil
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
        // Computed once regardless of style so the .usage fallback below can
        // check its actual success/failure instead of re-deriving it from a
        // raw session count (see shouldFallBackToDotsForUsageStyle's doc).
        let computedDotsImage = MenuBarStatusRenderer.buildStatusImage(
            sessions: nonGtSessions,
            projectGroupOrder: sessionManager.projectGroupOrder
        )
        let quotaImage = style == .lights ? nil : QuotaMenuBarRenderer.imageForSelectedProvider(
            sessions: nonGtSessions,
            providerKey: MenuBarQuotaProvider.current
        )
        // .usage style with sessions active but no renderable quota yet
        // (fresh daemon start, or the selected provider hasn't ticked a
        // statusline sample) must not collapse to nothing here — with
        // dotsImage nil and quotaImage nil, the caller falls through to
        // OffFlameImage.menuBar, the "no sessions running" icon, while
        // sessions are in fact active. Fall back to dots so the icon stays
        // honest about "something is running" even without quota data.
        let dotsImage = style != .usage || shouldFallBackToDotsForUsageStyle(
            style: style, quotaImage: quotaImage, dotsImage: computedDotsImage
        ) ? computedDotsImage : nil
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
