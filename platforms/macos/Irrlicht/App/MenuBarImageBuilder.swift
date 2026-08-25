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
    ///
    /// An errored session (#1802) deliberately does NOT promote to
    /// `.attention`. It was considered and rejected: `.attention` is a FULL
    /// REPLACEMENT of the dots, so routing errors through it would hide the
    /// red circle at exactly the moment there is one to show — the opposite of
    /// what the feature was asked for. The error signal is the red dot itself.
    static func iconState(pendingConsentCount: Int, sessionCount: Int) -> IconState {
        if pendingConsentCount > 0 { return .attention }
        if sessionCount > 0 { return .dots }
        return .off
    }

    /// Whether any session that will actually be DRAWN as a dot is errored.
    ///
    /// The filter has to match what `MenuBarStatusRenderer` draws, or the icon
    /// widens to point at a red circle that is not in it. Subagents are the
    /// trap: they live in `sessionManager.sessions` but
    /// `MenuBarStatusRenderer.orderedProjectGroups` excludes them
    /// (`where session.parentSessionId == nil`), so a failed CHILD would
    /// otherwise widen the icon while adding no red dot anywhere.
    ///
    /// Still an over-approximation in one known case: a group past
    /// `maxVisibleGroups` collapses into the grey overflow ellipsis, so an
    /// error only in the sixth project widens the icon without adding a dot.
    /// Narrowing that would mean re-deriving the whole grouping here, and the
    /// cost is one ellipsis rather than a missed error.
    ///
    /// Pure, so it is testable without a SessionManager — the same reason
    /// `iconState` and `shouldShowDotsInUsageStyle` are.
    static func hasErroredDot(in sessions: [SessionState]) -> Bool {
        sessions.contains { $0.parentSessionId == nil && $0.state == .error }
    }

    /// Whether `.usage` style should render the session dots after all.
    ///
    /// Two independent reasons, both of which end with the icon lying about
    /// the world if the dots stay hidden:
    ///
    ///   - **No renderable quota yet** (fresh daemon start, or the selected
    ///     provider hasn't ticked a statusline sample). With both halves nil
    ///     the caller falls through to `OffFlameImage.menuBar`, the "no
    ///     sessions running" icon, while sessions are in fact active.
    ///   - **A session is errored** (#1802). `.usage` suppresses the dots
    ///     outright, so the red circle this feature exists to show would be
    ///     invisible for every user on that style — the feature would silently
    ///     do nothing for them. Here the dots are ADDED rather than
    ///     substituted: `composeSideBySide` renders both halves, so the user's
    ///     chosen quota bars are kept and the red dot appears beside them. The
    ///     icon widens while an error stands, which is the intended cost — an
    ///     alert that never changes the icon's footprint is one nobody notices.
    ///
    /// Takes the already-built dots image rather than a raw session count: a
    /// non-zero session count doesn't guarantee `buildStatusImage` succeeds
    /// (e.g. sessions whose parent was pruned out from under them still carry a
    /// non-nil `parentSessionId` and get excluded from every project group),
    /// and checking the actual image avoids re-deriving that success/failure a
    /// second time. Pure decision, extracted for testability without a
    /// SessionManager, mirroring `iconState`.
    static func shouldShowDotsInUsageStyle(
        style: MenuBarStyle,
        quotaImage: NSImage?,
        dotsImage: NSImage?,
        hasErroredSession: Bool
    ) -> Bool {
        guard style == .usage, dotsImage != nil else { return false }
        return quotaImage == nil || hasErroredSession
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
        // raw session count (see shouldShowDotsInUsageStyle's doc).
        let computedDotsImage = MenuBarStatusRenderer.buildStatusImage(
            sessions: nonGtSessions,
            projectGroupOrder: sessionManager.projectGroupOrder
        )
        // Combined style shares its width budget with the dots, so the
        // quota bars render in a narrower, label-less layout there — see
        // QuotaMenuBarRenderer.buildSVG's `compact` handling.
        //
        // `now:` is the menu-bar icon's one wall-clock read (#1675). The icon is
        // rasterised into an `NSStatusItem`, not rendered by SwiftUI, so
        // `\.formatNow` cannot reach it — the clock has to be read somewhere and
        // this is that place, once, visibly, rather than twice inside
        // `rowSVG` where the 5h and 7d rows could be paced against two
        // different instants.
        let quotaImage = style == .lights ? nil : QuotaMenuBarRenderer.imageForSelectedProvider(
            sessions: nonGtSessions,
            providerKey: MenuBarQuotaProvider.current,
            compact: style == .combined,
            now: Date()
        )
        // .usage style hides the dots by default. Two conditions bring them
        // back — no renderable quota, or an errored session that would
        // otherwise have nowhere to be red. See shouldShowDotsInUsageStyle.
        //
        // `nonGtSessions` has already dropped Gas Town sessions; hasErroredDot
        // drops subagents. Both filters are needed — see its doc.
        let hasErroredSession = hasErroredDot(in: nonGtSessions)
        let dotsImage = style != .usage || shouldShowDotsInUsageStyle(
            style: style, quotaImage: quotaImage, dotsImage: computedDotsImage,
            hasErroredSession: hasErroredSession
        ) ? computedDotsImage : nil
        // Dots first (left), quota bars last (right) — closest to the
        // system status icons (WiFi/battery/clock), matching issue #909's
        // mockup ordering. Uses the same gap as between dot-groups
        // themselves (MenuBarStatusRenderer.groupGap) so the dots-to-quota
        // seam in Combined style reads as "one more group," not a wider gap.
        let baseImage = composeSideBySide(dotsImage, quotaImage, gap: MenuBarStatusRenderer.groupGap)

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
