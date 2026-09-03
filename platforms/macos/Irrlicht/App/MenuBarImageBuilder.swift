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

    /// Whether `.usage` style should render the session dots after all.
    ///
    /// One reason, and it is the case where hiding the dots would make the
    /// icon lie about the world: **no renderable quota yet** (fresh daemon
    /// start, or the selected provider hasn't ticked a statusline sample).
    /// With both halves nil the caller falls through to
    /// `OffFlameImage.menuBar`, the "no sessions running" icon, while
    /// sessions are in fact active.
    ///
    /// #1802 added a second arm — bring the dots back whenever a session is
    /// errored, so `.usage` would still show the red circle. #1862 removed
    /// it: the caller re-adds the WHOLE dot bank (`computedDotsImage` in
    /// `iconImage`), not just the errored project, and an `error` state does
    /// not clear on a timer — so the escalation had become the normal view
    /// for every `.usage` user rather than a rare alert. `.usage` now honors
    /// its own style strictly: quota bars only, error or not. That leaves
    /// #1802's error signal with no surface on this style (tracked as a
    /// follow-up to #1862).
    ///
    /// Takes the already-built dots image rather than a raw session count: a
    /// non-zero session count doesn't guarantee `buildStatusImage` succeeds
    /// (e.g. sessions whose parent was pruned out from under them still carry a
    /// non-nil `parentSessionId` and get excluded from every project group),
    /// and checking the actual image avoids re-deriving that success/failure a
    /// second time. Pure decision, extracted for testability without a
    /// SessionManager, mirroring `iconState`.
    static func shouldShowDotsInUsageStyle(
        appearance: MenuBarAppearance,
        quotaImage: NSImage?,
        dotsImage: NSImage?
    ) -> Bool {
        guard appearance.hidesDotsWhenQuotaIsRenderable, dotsImage != nil else { return false }
        return quotaImage == nil
    }

    /// Whether the dot half is drawn at all — the WHOLE decision, not just the
    /// `.usage` arm of it.
    ///
    /// Extracted from `combinedImage` (#1852) for the same reason `dotsImage`
    /// and `quotaImage` were extracted in #1849, and for a reason that was
    /// measured rather than assumed: spelled inline, swapping the first
    /// predicate for its neighbour left the suite green while making `.usage`
    /// draw its dots unconditionally. `combinedImage` is private and needs a
    /// live `SessionManager`, so only the extracted form is reachable from a
    /// test — see
    /// `MenuBarImageBuilderTests.testShowsDotsAcrossEveryStyleAndDensity`,
    /// which carries the exact mutation.
    static func showsDots(
        appearance: MenuBarAppearance,
        quotaImage: NSImage?,
        dotsImage: NSImage?
    ) -> Bool {
        guard appearance.hidesDotsWhenQuotaIsRenderable else { return true }
        return shouldShowDotsInUsageStyle(
            appearance: appearance,
            quotaImage: quotaImage,
            dotsImage: dotsImage
        )
    }

    /// The dot half of the icon, for a given appearance.
    ///
    /// Extracted from `combinedImage` so the appearance-to-renderer routing is
    /// reachable from a test without a `SessionManager` — the same reason
    /// `iconState` and `shouldShowDotsInUsageStyle` are pure. Review of
    /// #1849 measured that this wiring was the one part of the change no
    /// test could see: inverting the condition here left all 527 tests
    /// green while collapsing every shipped style to a single dot.
    ///
    /// Since #1852 the aggregate path is reachable from every style, not just
    /// the one that hard-coded it — `aggregatesSessionDots` is the modifier,
    /// not a property of what the icon shows.
    static func dotsImage(
        appearance: MenuBarAppearance,
        sessions: [SessionState],
        projectGroupOrder: [String]
    ) -> NSImage? {
        appearance.aggregatesSessionDots
            ? MenuBarStatusRenderer.buildAggregateStatusImage(sessions: sessions)
            : MenuBarStatusRenderer.buildStatusImage(
                sessions: sessions,
                projectGroupOrder: projectGroupOrder
            )
    }

    /// The quota half of the icon, for a given appearance — nil when the style
    /// carries no quota bars at all. Extracted for the same reason as
    /// `dotsImage`: swapping `usesNarrowQuotaBars` for `showsQuotaBars` in
    /// the `compact:` argument is a one-word slip that silently re-lays-out
    /// every `.usage` user's icon, and nothing could see it.
    static func quotaImage(
        appearance: MenuBarAppearance,
        sessions: [SessionState],
        providerKey: String?,
        now: Date
    ) -> NSImage? {
        guard appearance.showsQuotaBars else { return nil }
        return QuotaMenuBarRenderer.imageForSelectedProvider(
            sessions: sessions,
            providerKey: providerKey,
            compact: appearance.usesNarrowQuotaBars,
            now: now
        )
    }

    /// The whole icon for one appearance: both halves, the `.usage` dot
    /// fallback, and the order they are composed in.
    ///
    /// **Why this is a separate function from `combinedImage`.** #1849
    /// extracted `dotsImage` and `quotaImage`, and #1852 extracted `showsDots`,
    /// each so the *decision* it makes could be reached by a test. But
    /// `combinedImage` needs a live `SessionManager` and a `GasTownProvider`,
    /// so no test can call it — which left everything the extracted seams were
    /// handed to, and everything done with what they returned, as unreachable
    /// as before. Review of #1852 measured that gap: four separate mutations
    /// inside the old inline composition survived the full suite. A
    /// source-text pin cannot see any of them, because it matches the call and
    /// not what the call is given or what is done with the result. So the
    /// composition moved here, where a test can assert the composed image
    /// itself. Each mutation, and the test that kills it, is recorded on
    /// `MenuBarImageBuilderTests.testComposedIconForEveryStyleAndModifier`,
    /// `...testUsageComposesDotsWhenNoQuotaIsRenderable` and
    /// `...testComposedIconPutsTheDotsBeforeTheQuotaBars`.
    ///
    /// `sessions` must already have Gas Town's sessions filtered out; the
    /// badge is composed by the caller.
    static func iconImage(
        appearance: MenuBarAppearance,
        sessions: [SessionState],
        projectGroupOrder: [String],
        providerKey: String?,
        now: Date
    ) -> NSImage? {
        // Computed once regardless of appearance so the .usage fallback below
        // can check its actual success/failure instead of re-deriving it from
        // a raw session count (see shouldShowDotsInUsageStyle's doc).
        //
        // The compact modifier (#1845, reshaped by #1852) collapses every
        // project into ONE aggregate dot, so the dot half's width stops
        // growing with the project count — on whichever style is selected.
        let computedDotsImage = dotsImage(
            appearance: appearance,
            sessions: sessions,
            projectGroupOrder: projectGroupOrder
        )
        // Combined style shares its width budget with the dots, so the
        // quota bars render in a narrower, label-less layout there — see
        // QuotaMenuBarRenderer.buildSVG's `compact` handling. Since #1852 the
        // Usage style reaches that same narrow layout via the compact
        // modifier; Combined stays narrow either way, which is the
        // compatibility bound (MenuBarAppearance.usesNarrowQuotaBars).
        let builtQuotaImage = quotaImage(
            appearance: appearance,
            sessions: sessions,
            providerKey: providerKey,
            now: now
        )
        // .usage style hides the dots by default, and brings them back only
        // when there is no renderable quota to show instead — see
        // shouldShowDotsInUsageStyle. (#1862: an errored session used to
        // bring them back too; that arm is gone — see that function's doc.)
        //
        // The locals are named `built…`/`shown…` rather than reusing the
        // helpers' own names: `let quotaImage = quotaImage(...)` compiles, but
        // it gives one identifier two meanings inside a single function and
        // reads like recursion at a glance.
        let shownDotsImage = showsDots(
            appearance: appearance, quotaImage: builtQuotaImage, dotsImage: computedDotsImage
        ) ? computedDotsImage : nil
        // Dots first (left), quota bars last (right) — closest to the
        // system status icons (WiFi/battery/clock), matching issue #909's
        // mockup ordering. Uses the same gap as between dot-groups
        // themselves (MenuBarStatusRenderer.groupGap) so the dots-to-quota
        // seam in Combined style reads as "one more group," not a wider gap.
        //
        // Either half may be nil (no sessions for .usage-only, or no
        // rate_limit data yet) without collapsing the whole icon —
        // composeSideBySide degrades to whichever half exists.
        return composeSideBySide(
            shownDotsImage, builtQuotaImage, gap: MenuBarStatusRenderer.groupGap
        )
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

        let baseImage = iconImage(
            appearance: MenuBarAppearance.current,
            sessions: nonGtSessions,
            projectGroupOrder: sessionManager.projectGroupOrder,
            providerKey: MenuBarQuotaProvider.current,
            // `now:` is the menu-bar icon's one wall-clock read (#1675). The
            // icon is rasterised into an `NSStatusItem`, not rendered by
            // SwiftUI, so `\.formatNow` cannot reach it — the clock has to be
            // read somewhere and this is that place, once, visibly, rather
            // than twice inside `rowSVG` where the 5h and 7d rows could be
            // paced against two different instants.
            now: Date()
        )

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
