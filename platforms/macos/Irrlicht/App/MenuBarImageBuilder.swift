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
        let dotsImage = MenuBarStatusRenderer.buildStatusImage(
            sessions: nonGtSessions,
            projectGroupOrder: sessionManager.projectGroupOrder
        )

        guard gasTownProvider.isDaemonRunning else { return dotsImage }

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
        let badgeSize = badge.size()

        let gap: CGFloat = dotsImage != nil ? 4 : 0
        let dotsWidth = dotsImage?.size.width ?? 0
        let dotsHeight = dotsImage?.size.height ?? 0
        let totalWidth = badgeSize.width + gap + dotsWidth
        let totalHeight = max(badgeSize.height, dotsHeight)

        let combined = NSImage(size: NSSize(width: totalWidth, height: totalHeight))
        combined.lockFocus()
        let badgeY = (totalHeight - badgeSize.height) / 2
        badge.draw(at: NSPoint(x: 0, y: badgeY))
        if let dotsImage {
            let dotsY = (totalHeight - dotsHeight) / 2
            dotsImage.draw(at: NSPoint(x: badgeSize.width + gap, y: dotsY),
                           from: .zero, operation: .sourceOver, fraction: 1)
        }
        combined.unlockFocus()
        combined.isTemplate = false
        return combined
    }

}
