import AppKit

@MainActor
enum MenuBarImageBuilder {
    static func build(
        sessionManager: SessionManager,
        gasTownProvider: GasTownProvider
    ) -> NSImage {
        if let combined = combinedImage(sessionManager: sessionManager, gasTownProvider: gasTownProvider) {
            return combined
        }
        return sparkleImage()
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

    private static func sparkleImage() -> NSImage {
        let config = NSImage.SymbolConfiguration(pointSize: 14, weight: .regular)
        let base = NSImage(systemSymbolName: "sparkle", accessibilityDescription: "Irrlicht")
            ?? NSImage()
        let configured = base.withSymbolConfiguration(config) ?? base
        configured.isTemplate = true
        return configured
    }
}
