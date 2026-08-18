import Foundation
@preconcurrency import UserNotifications

// MARK: - Notifications: context pressure + state transitions
//
// Split out of SessionManager.swift (#807): everything that decides whether
// to fire a macOS notification (context-pressure alerts, ready/waiting state
// transitions) and the pure sound/voice lookups those decisions depend on.

extension SessionManager {
    var canUseUserNotifications: Bool {
        guard ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil else {
            return false
        }
        guard Bundle.main.bundleIdentifier != nil else {
            return false
        }
        return Bundle.main.bundleURL.pathExtension == "app"
    }

    /// Registers the notification click-forwarder delegate. Does NOT request
    /// macOS notification authorization — events default to off, and the
    /// permission prompt fires from Settings when the user first switches a
    /// notification on (#425, `SettingsView.checkNotificationAuth`).
    func setupNotificationDelegate() {
        guard canUseUserNotifications else {
            print("⚠️ Skipping notification setup outside app bundle")
            return
        }

        // Register the click-forwarder delegate before the first notification
        // is scheduled, otherwise macOS drops notification taps silently.
        if notificationForwarder == nil {
            let forwarder = NotificationClickForwarder(
                onTap: { [weak self] sessionID in
                    self?.handleNotificationTap(sessionID: sessionID)
                },
                focusMonitor: focusMonitor
            )
            notificationForwarder = forwarder
            UNUserNotificationCenter.current().delegate = forwarder
        }
    }

    // MARK: - Context Pressure Notifications

    /// Checks active sessions for context usage reaching the configured alert
    /// threshold (#689). Fires a macOS notification the first time the threshold
    /// is reached per session. The fired-set is keyed by the active threshold
    /// value, so changing the setting naturally re-arms the alert.
    func checkContextPressureAlerts(sessions: [SessionState]) {
        let threshold = ContextPressureThreshold.current
        let key = Int(threshold.value)
        for session in sessions {
            guard session.state == .working || session.state == .waiting,
                  let metrics = session.metrics,
                  threshold.isExceeded(by: metrics) else { continue }

            var fired = notifiedThresholds[session.id] ?? Set<Int>()
            guard !fired.contains(key) else { continue }
            fired.insert(key)
            notifiedThresholds[session.id] = fired
            sendContextPressureNotification(session: session, threshold: threshold, metrics: metrics)
        }
    }

    func sendContextPressureNotification(session: SessionState, threshold: ContextPressureThreshold, metrics: SessionMetrics) {
        guard defaults.bool(forKey: NotificationEvent.contextPressure.enabledKey) else { return }
        let label = session.projectName ?? session.shortId

        let title: String
        switch threshold.unit {
        case .percent:
            title = "Context pressure: \(Int(threshold.value))% threshold reached"
        case .tokens:
            title = "Context pressure: \(Self.formatTokens(Int64(threshold.value))) tokens reached"
        }

        // Prefer the percentage when the model's context window is known,
        // otherwise report the raw token count so token-mode alerts on
        // unknown-window models still read sensibly.
        let usage = metrics.contextUtilization > 0
            ? "at \(String(format: "%.1f%%", metrics.contextUtilization)) context"
            : "at \(Self.formatTokens(metrics.totalTokens)) tokens"

        sendNotification(
            identifier: "irrlicht-context-\(session.id)-\(Int(threshold.value))",
            title: title,
            body: "\(label) is \(usage). Consider switching to a fresh session.",
            sessionID: session.id,
            event: .contextPressure
        )
    }

    /// Compact token count for notification copy (e.g. 150000 → "150K").
    static func formatTokens(_ count: Int64) -> String {
        if count < 1000 { return "\(count)" }
        if count < 1_000_000 { return String(format: "%.0fK", Double(count) / 1000) }
        return String(format: "%.1fM", Double(count) / 1_000_000)
    }

    // MARK: - State Transition Notifications

    func checkStateTransitionNotification(session: SessionState, previousState: SessionState.State) {
        // Skip subagent sessions to avoid notification noise
        if session.parentSessionId != nil { return }

        let notifyReady = defaults.bool(forKey: "notifyOnReady")
        let notifyWaiting = defaults.bool(forKey: "notifyOnWaiting")

        let title: String
        let event: NotificationEvent
        switch session.state {
        case .ready where notifyReady && previousState == .working:
            title = "Agent ready"
            event = .ready
        case .waiting where notifyWaiting && previousState == .working:
            title = "Agent waiting for input"
            event = .waiting
        default:
            return
        }

        let label = session.projectName ?? session.shortId
        let branch = session.gitBranch.map { " (\($0))" } ?? ""

        sendNotification(
            identifier: "irrlicht-state-\(session.id)",
            title: title,
            body: "\(label)\(branch)",
            sessionID: session.id,
            event: event
        )
    }

    func sendNotification(
        identifier: String,
        title: String,
        body: String,
        sessionID: String,
        event: NotificationEvent
    ) {
        // Master gate (#1183). Every notification — banner, sound and the TTS
        // below — leaves through here, so one guard silences all of them and no
        // future caller can route around it. Gating here rather than at the two
        // call sites is also why the spoken voice, which bypasses
        // UNNotificationContent entirely, is covered.
        //
        // `defaults` is passed, and `masterEnabled` no longer takes a default
        // for it (#1693): this line used to call `masterEnabled()`, whose
        // default argument was `UserDefaults.standard`, so one method decided
        // WHETHER to notify from the machine and read WHICH sound from its
        // input — the store `checkStateTransitionNotification` had just read
        // `notifyOnReady`/`notifyOnWaiting` from two frames up.
        guard NotificationSettings.masterEnabled(defaults: defaults) else { return }
        guard canUseUserNotifications else { return }
        let choice = Self.choice(for: event, defaults: defaults)
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = Self.notificationSound(for: choice)
        content.userInfo = [NotificationUserInfoKey.sessionID: sessionID]

        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                print("⚠️ Failed to send notification: \(error.localizedDescription)")
            }
        }

        // willPresent is unreliable for LSUIElement menubar-only apps — macOS
        // skips it when it considers the app not-in-foreground, and Irrlicht
        // sits in that grey zone. Drive speech from here (on @MainActor) so
        // it actually fires for real state transitions. The master gate above
        // plus the per-event toggle are the off switches; users who pick a
        // Speak aloud variant have opted in.
        // TTS bypasses UNNotificationContent entirely, so we must also gate on
        // Focus here — otherwise the loudest sound option leaks through DND.
        if let voice = Self.voiceForSpeak(choice: choice, focusActive: focusMonitor.isFocusActive) {
            SoundPlayer.speak(title: title, body: body, voice: voice)
        }
    }

    /// Pure decision helper: returns the voice to speak with when the chosen
    /// SoundChoice is `.speak(_)` AND Focus is not active. Anything else → nil.
    /// Extracted so the Focus-gating branch in `sendNotification` has direct
    /// unit-test coverage without needing to stub `SoundPlayer`.
    nonisolated static func voiceForSpeak(choice: SoundChoice, focusActive: Bool) -> SpokenVoice? {
        if case .speak(let voice) = choice, !focusActive { return voice }
        return nil
    }

    /// Pure: turn a SoundChoice into a UNNotificationSound. `.none` / `.speak`
    /// → nil (no audible alert from the notification center). A `.custom`
    /// choice whose installed file went missing falls back to Ping.
    nonisolated static func notificationSound(for choice: SoundChoice) -> UNNotificationSound? {
        switch choice {
        case .none, .speak:
            return nil
        case .custom(let installedFilename, _):
            let path = AppHome.library
                .appendingPathComponent("Sounds")
                .appendingPathComponent(installedFilename).path
            if FileManager.default.fileExists(atPath: path) {
                return UNNotificationSound(named: UNNotificationSoundName(installedFilename))
            }
            return UNNotificationSound(named: UNNotificationSoundName("Ping.aiff"))
        default:
            guard let name = choice.notificationSoundName else { return .default }
            return UNNotificationSound(named: UNNotificationSoundName(name))
        }
    }

    /// Convenience for tests + callers that want the event → sound lookup in
    /// a single hop. Production path uses `choice(for:)` + `notificationSound(for:)`
    /// directly to avoid double-reading UserDefaults.
    nonisolated static func resolveNotificationSound(
        for event: NotificationEvent,
        defaults: UserDefaults
    ) -> UNNotificationSound? {
        notificationSound(for: choice(for: event, defaults: defaults))
    }

    /// `defaults` is a parameter rather than `.standard` for #1662's reason:
    /// a test driving this must not have to write the developer's real
    /// `com.apple.dt.xctest.tool` domain and put it back in a `tearDown` the
    /// aborting runs skip.
    ///
    /// It carries no DEFAULT for #1693's reason, which is the sibling of that
    /// one: a store that can be omitted is a store nobody has to think about,
    /// and the omission reads as "the caller decided" where it means "the
    /// caller did not". `masterEnabled(defaults:)` above lost its default in
    /// the same change — these two were the notification path's remaining
    /// `UserDefaults = .standard` parameter defaults, both with zero callers
    /// relying on them, so removal was free and the compiler now holds what a
    /// lint rule would have had to.
    nonisolated static func choice(
        for event: NotificationEvent,
        defaults: UserDefaults
    ) -> SoundChoice {
        let raw = defaults.string(forKey: event.soundKey) ?? SoundChoice.default.rawValue
        return SoundChoice(rawValue: raw) ?? .default
    }

    /// Invoked by `NotificationClickForwarder` on the main actor when the user
    /// taps a notification. Silently no-ops for unknown IDs (e.g. a stale
    /// notification for a session that's since been deleted).
    private func handleNotificationTap(sessionID: String) {
        guard let session = sessionMap[sessionID] else { return }
        SessionLauncher.jump(session)
    }
}

/// Keys used in UNNotificationContent.userInfo so notification click-handlers
/// can identify the originating session.
enum NotificationUserInfoKey {
    static let sessionID = "sessionID"
}

/// NSObject-based forwarder that receives `UNUserNotificationCenterDelegate`
/// callbacks and hands them off to a closure on the main actor. Used instead
/// of making `SessionManager` itself inherit from NSObject.
final class NotificationClickForwarder: NSObject, UNUserNotificationCenterDelegate {
    private let onTap: @MainActor (String) -> Void
    private let focusMonitor: FocusStateProviding

    init(onTap: @escaping @MainActor (String) -> Void, focusMonitor: FocusStateProviding) {
        self.onTap = onTap
        self.focusMonitor = focusMonitor
    }

    nonisolated func userNotificationCenter(
        _: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let sessionID = response.notification.request.content.userInfo[NotificationUserInfoKey.sessionID] as? String
        if let sessionID {
            Task { @MainActor in
                onTap(sessionID)
            }
        }
        completionHandler()
    }

    // Show banners for notifications delivered while the app is foregrounded
    // — without this, in-app notifications are silently suppressed on macOS.
    // Under Focus / DND, suppress both banner and sound: the user has asked
    // macOS for quiet, and forcing `.sound` here is what lets the sound leak
    // through even when the OS is suppressing the banner.
    nonisolated func userNotificationCenter(
        _: UNUserNotificationCenter,
        willPresent _: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler(Self.presentationOptions(focusActive: focusMonitor.isFocusActive))
    }

    /// Pure decision helper for `willPresent`. Extracted so tests can verify
    /// the Focus-gating branch without needing a real UNUserNotificationCenter
    /// instance (which can't be constructed outside an app bundle).
    nonisolated static func presentationOptions(focusActive: Bool) -> UNNotificationPresentationOptions {
        focusActive ? [] : [.banner, .sound]
    }
}
