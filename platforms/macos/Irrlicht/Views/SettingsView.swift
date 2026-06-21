import ServiceManagement
import SwiftUI
import UniformTypeIdentifiers
import UserNotifications

struct SettingsView: View {
    @Binding var isPresented: Bool
    /// Opens the permission wizard in review mode (issue #570). Wired by
    /// SessionListView, which owns the panel-body swap.
    var onReviewPermissions: () -> Void = {}
    @EnvironmentObject var updateManager: UpdateManager
    @EnvironmentObject var sessionManager: SessionManager
    @EnvironmentObject var daemonManager: DaemonManager
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("showQuotaForecast") private var showQuotaForecast: Bool = true
    @AppStorage("launchAtLogin") private var launchAtLogin: Bool = true
    @AppStorage("taskEtaActivation") private var taskEtaActivation: Bool = false
    // Advanced Settings disclosure state (#694) — collapsed by default; the
    // power-user / still-maturing controls (debug, task-eta, sources, CLI tool)
    // live under it.
    @AppStorage("advancedSettingsExpanded") private var advancedSettingsExpanded: Bool = false
    @AppStorage("providerMode_anthropic") private var providerModeAnthropic: String = ProviderModePreference.auto.rawValue
    @AppStorage("providerMode_openai") private var providerModeOpenAI: String = ProviderModePreference.auto.rawValue
    @AppStorage(NotificationEvent.ready.enabledKey) private var notifyOnReady: Bool = false
    @AppStorage(NotificationEvent.waiting.enabledKey) private var notifyOnWaiting: Bool = false
    @AppStorage(NotificationEvent.contextPressure.enabledKey) private var notifyOnContextPressure: Bool = false
    // Sources (multi-source): mirror the web dashboard's keys.
    @AppStorage("useLocalDaemon") private var useLocalDaemon: Bool = true
    @AppStorage("useRelayServer") private var useRelayServer: Bool = false
    // Publish direction (issue #718): forward this Mac's sessions to the relay.
    // Independent of useRelayServer (subscribe); both share relayServerURL + the
    // Keychain token.
    @AppStorage("publishToRelay") private var publishToRelay: Bool = false
    @AppStorage("relayServerURL") private var relayServerURL: String = ""
    @State private var relayURLDraft: String = ""
    @State private var relayURLDebounceTask: Task<Void, Never>? = nil
    // Live publish-link state, polled from the daemon's /api/v1/relay/publish.
    @StateObject private var publishStatus = PublishStatusMonitor()
    // Set just before a programmatic reconcile of taskEtaActivation so the
    // .onChange it triggers is swallowed instead of re-POSTing to the daemon.
    @State private var taskEtaReconciling = false
    // Relay bearer token lives in the Keychain (not @AppStorage); this draft
    // mirrors it for the field, loaded on appear and written on change.
    @State private var relayTokenDraft: String = ""
    @State private var notificationsDenied = false
    @State private var loginItemStatus: SMAppService.Status = .notRegistered
    @State private var customImportError: String?

    var body: some View {
        // Header and footer are pinned; only the settings sections scroll.
        // This keeps the "Done" button visible no matter how many sections
        // expand — the old fixed `height: 860` frame overflowed the menu-bar
        // popover and clipped the footer. The scroll area is capped the same
        // way SessionListView caps its list (see `groupListMaxHeight`).
        // Horizontal insets live on the header, footer, scroll *content*, and
        // dividers — NOT on the outer container — so the ScrollView spans the
        // full panel width and its scroll track sits flush against the right
        // edge, while the text stays inset.
        VStack(alignment: .leading, spacing: 0) {
            Text("Settings")
                .font(.headline)
                .padding(.horizontal, 20)
                .padding(.top, 20)
                .padding(.bottom, 12)

            Divider()
                .padding(.horizontal, 20)

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    LeadingToggle(
                        isOn: $showCostDisplay,
                        label: "Show Estimated Cost",
                        info: "Display estimated USD cost per session and per project group. Cost estimates are approximate."
                    )

                    VStack(alignment: .leading, spacing: 8) {
                        LeadingToggle(
                            isOn: $showQuotaForecast,
                            label: "Show Quota Forecast",
                            info: "Replace the app version in the header with a live burn-rate forecast against your Pro/Max or ChatGPT subscription cap. Only appears when a subscription session is active."
                        )

                        if showQuotaForecast {
                            HStack(spacing: 6) {
                                Text("Provider")
                                    .font(.caption)
                                    .fontWeight(.medium)
                                    .foregroundColor(.secondary)
                                InfoIcon(text: "Auto picks the chip variant from the snapshot; override when you have multiple paths into one provider.")
                                Spacer()
                            }
                            providerModeRow(label: "Anthropic", selection: $providerModeAnthropic)
                            providerModeRow(label: "OpenAI", selection: $providerModeOpenAI)
                        }
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        LeadingToggle(
                            isOn: $launchAtLogin,
                            label: "Open at Login",
                            info: "Start Irrlicht automatically when you log in to your Mac."
                        )
                        .onChange(of: launchAtLogin) { newValue in
                            LoginItemManager.setEnabled(newValue)
                            // setEnabled runs detached; give launchd a beat,
                            // then re-read the system's real status.
                            refreshLoginItemStatus(after: 0.4)
                        }

                        if loginItemStatus == .requiresApproval {
                            HStack(spacing: 4) {
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundColor(.orange)
                                    .font(.caption)
                                    .tooltip("Login item needs approval in System Settings")
                                Text("Approve Irrlicht in Login Items.")
                                    .font(.caption)
                                    .foregroundColor(.orange)
                                Button("Open Login Items") {
                                    LoginItemManager.openLoginItemsSettings()
                                }
                                .font(.caption)
                                .buttonStyle(.link)
                                .tooltip("Open System Settings → General → Login Items")
                            }
                        }
                    }
                    .onAppear { refreshLoginItemStatus() }

                    Divider()

                    VStack(alignment: .leading, spacing: 8) {
                        HStack(spacing: 6) {
                            Text("Permissions")
                                .font(.subheadline)
                                .fontWeight(.medium)
                            InfoIcon(text: "Everything irrlicht may read or modify, per agent. Toggling a grant off undoes the modification and stops all reading.")
                            Spacer()
                        }
                        Button("Review agent permissions…") {
                            onReviewPermissions()
                        }
                        .controlSize(.small)
                        .tooltip("Open the per-agent permission toggles")
                    }

                    Divider()

                    VStack(alignment: .leading, spacing: 10) {
                        HStack(spacing: 6) {
                            Text("Notifications")
                                .font(.subheadline)
                                .fontWeight(.medium)
                            InfoIcon(text: "Pick a sound per event, choose your own audio file, or have the message read aloud.")
                            Spacer()
                        }

                        NotificationEventRow(
                            event: .ready,
                            enabled: $notifyOnReady,
                            sampleText: "Agent ready",
                            onImportError: { customImportError = $0 }
                        )
                        NotificationEventRow(
                            event: .waiting,
                            enabled: $notifyOnWaiting,
                            sampleText: "Agent waiting for input",
                            onImportError: { customImportError = $0 }
                        )
                        NotificationEventRow(
                            event: .contextPressure,
                            enabled: $notifyOnContextPressure,
                            sampleText: "Context pressure: 80% threshold reached",
                            onImportError: { customImportError = $0 }
                        )
                        ContextThresholdRow(enabled: notifyOnContextPressure)
                            .padding(.leading, 18)

                        if let error = customImportError {
                            Text(error)
                                .font(.caption)
                                .foregroundColor(.orange)
                                .fixedSize(horizontal: false, vertical: true)
                        }

                        if notificationsDenied && (notifyOnReady || notifyOnWaiting || notifyOnContextPressure) {
                            HStack(spacing: 4) {
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundColor(.orange)
                                    .font(.caption)
                                    .tooltip("Notifications blocked in System Settings")
                                Text("Notifications are blocked.")
                                    .font(.caption)
                                    .foregroundColor(.orange)
                                Button("Open Settings") {
                                    if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings") {
                                        NSWorkspace.shared.open(url)
                                    }
                                }
                                .font(.caption)
                                .buttonStyle(.link)
                                .tooltip("Open System Settings → Notifications")
                            }
                        }
                    }
                    .onAppear { checkNotificationAuth() }
                    .onChange(of: notifyOnReady) { _ in checkNotificationAuth() }
                    .onChange(of: notifyOnWaiting) { _ in checkNotificationAuth() }
                    .onChange(of: notifyOnContextPressure) { _ in checkNotificationAuth() }

                    Divider()

                    VStack(alignment: .leading, spacing: 10) {
                        Text("Updates")
                            .font(.subheadline)
                            .fontWeight(.medium)

                        LeadingToggle(
                            isOn: $updateManager.automaticallyChecksForUpdates,
                            label: "Automatically check for updates"
                        )

                        HStack {
                            Text("Current version")
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Spacer()
                            Text(appVersion)
                                .font(.caption)
                                .foregroundColor(.secondary)
                                .monospacedDigit()
                        }

                        HStack {
                            Text("Last checked")
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Spacer()
                            Text(lastCheckedDescription)
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }

                        Button("Check for Updates…") {
                            updateManager.checkForUpdates()
                        }
                        .controlSize(.small)
                    }
                    .onAppear { updateManager.refresh() }

                    Divider()

                    // Advanced Settings (#694): power-user and still-maturing
                    // controls, collapsed by default. Disclosure state persists
                    // via @AppStorage("advancedSettingsExpanded").
                    VStack(alignment: .leading, spacing: 12) {
                        Button {
                            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                                advancedSettingsExpanded.toggle()
                            }
                        } label: {
                            HStack(spacing: 6) {
                                Image(systemName: "chevron.right")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundColor(.secondary)
                                    .rotationEffect(.degrees(advancedSettingsExpanded ? 90 : 0))
                                Text("Advanced Settings")
                                    .font(.subheadline)
                                    .fontWeight(.medium)
                                Spacer()
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .tooltip("Show debug, task-estimate markers, sources, and the command-line tool")
                        .accessibilityElement(children: .combine)
                        .accessibilityValue(advancedSettingsExpanded ? "expanded" : "collapsed")

                        if advancedSettingsExpanded {
                            LeadingToggle(
                                isOn: $debugMode,
                                label: "Debug Mode",
                                info: "Show session IDs, creation time, and time since last update."
                            )

                            // Task-eta global activation (issue #558). The daemon is
                            // the source of truth: the toggle posts the flip and then
                            // mirrors the daemon's answer, so a failed install never
                            // shows as "on".
                            LeadingToggle(
                                isOn: $taskEtaActivation,
                                label: "Task-Estimate Markers",
                                info: "Add a managed emission rule to ~/.claude/CLAUDE.md so agents report task progress and sessions show a completion ETA. Only the Irrlicht-managed block is written; the rest of the file is untouched. Turning this off removes the block.",
                                beta: true
                            )
                            .onChange(of: taskEtaActivation) { newValue in
                                // Swallow the change we made ourselves while reconciling
                                // from the daemon — only a real user toggle should write.
                                if taskEtaReconciling { taskEtaReconciling = false; return }
                                Task {
                                    // nil = daemon unreachable: leave the toggle where the
                                    // user put it; the next open reconciles. Only correct
                                    // the toggle on a definite, differing answer.
                                    if let actual = await ActivationClient.set(enabled: newValue), actual != newValue {
                                        taskEtaReconciling = true
                                        taskEtaActivation = actual
                                    }
                                }
                            }
                            .onAppear {
                                Task {
                                    // Only reconcile on a definite answer — an unreachable
                                    // daemon (nil) must NOT flip the toggle off and trigger
                                    // a spurious uninstall of the managed CLAUDE.md block.
                                    if let actual = await ActivationClient.status(), actual != taskEtaActivation {
                                        taskEtaReconciling = true
                                        taskEtaActivation = actual
                                    }
                                }
                            }

                            VStack(alignment: .leading, spacing: 8) {
                                HStack(spacing: 6) {
                                    Text("Sources")
                                        .font(.subheadline)
                                        .fontWeight(.medium)
                                    BetaBadge()
                                    Spacer()
                                }

                                LeadingToggle(
                                    isOn: $useLocalDaemon,
                                    label: "Local",
                                    info: "Watch the daemon this app connects to directly."
                                )

                                LeadingToggle(
                                    isOn: $useRelayServer,
                                    label: "Relay server",
                                    info: "Also connect to a relay to see sessions from other machines."
                                )

                                LeadingToggle(
                                    isOn: $publishToRelay,
                                    label: "Publish to relay",
                                    info: "Forward this Mac's sessions to the relay so machines signed in with a token from the same account can see them. Keeps serving locally too."
                                )

                                // URL + token are shared by both directions: show
                                // them whenever either subscribe or publish is on.
                                if useRelayServer || publishToRelay {
                                    HStack(spacing: 6) {
                                        TextField("ws://localhost:7839", text: $relayURLDraft)
                                            .textFieldStyle(.roundedBorder)
                                            .font(.system(.caption, design: .monospaced))
                                            .autocorrectionDisabled(true)
                                            .onChange(of: relayURLDraft) { newValue in
                                                relayURLDebounceTask?.cancel()
                                                relayURLDebounceTask = Task {
                                                    try? await Task.sleep(nanoseconds: 600_000_000)
                                                    guard !Task.isCancelled else { return }
                                                    relayServerURL = newValue.trimmingCharacters(in: .whitespacesAndNewlines)
                                                    // POST the new URL to the running daemon so it
                                                    // reconfigures its forwarder live (issue #722).
                                                    daemonManager.publishSettingsDidChange()
                                                }
                                            }
                                        if useRelayServer {
                                            Circle()
                                                .fill(sessionManager.relayConnectionState.dotColor)
                                                .frame(width: 8, height: 8)
                                                .tooltip("Subscribe: \(sessionManager.relayConnectionState.shortLabel)")
                                        }
                                    }
                                    SecureField("Relay token (leave empty if the relay has no auth)", text: $relayTokenDraft)
                                        .textFieldStyle(.roundedBorder)
                                        .font(.system(.caption, design: .monospaced))
                                        .autocorrectionDisabled(true)
                                        .onChange(of: relayTokenDraft) { newValue in
                                            KeychainStore.set(newValue.trimmingCharacters(in: .whitespacesAndNewlines), account: "relayToken")
                                            // Keychain writes don't fire UserDefaults.didChangeNotification,
                                            // so nudge both directions to pick up the new token: the
                                            // subscribe link reconnects, and we POST the new token to the
                                            // daemon so it republishes with it (issue #722).
                                            sessionManager.relayTokenDidChange()
                                            daemonManager.publishSettingsDidChange()
                                        }

                                    if publishToRelay {
                                        HStack(spacing: 6) {
                                            Circle()
                                                .fill(publishStatus.state.dotColor)
                                                .frame(width: 8, height: 8)
                                            Text("Publishing — \(publishStatus.state.label)")
                                                .font(.caption)
                                                .foregroundColor(.secondary)
                                            Spacer()
                                        }
                                    }

                                    Text("Publish and subscribe must use tokens from the same account to see each other's sessions.")
                                        .font(.caption)
                                        .foregroundColor(.secondary)
                                }
                            }
                            .onAppear {
                                relayURLDraft = relayServerURL
                                relayTokenDraft = KeychainStore.get(account: "relayToken")
                                if publishToRelay { publishStatus.start() }
                            }
                            .onDisappear {
                                publishStatus.stop()
                            }
                            .onChange(of: useRelayServer) { on in
                                if on && relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                    relayURLDraft = "ws://localhost:7839"
                                }
                                commitRelayURL()
                            }
                            .onChange(of: publishToRelay) { on in
                                if on && relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                    relayURLDraft = "ws://localhost:7839"
                                }
                                commitRelayURL()
                                daemonManager.publishSettingsDidChange()
                                if on { publishStatus.start() } else { publishStatus.stop() }
                            }

                            CLIToolSection()
                        }
                    }
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 16)
            }
            .frame(maxHeight: 520)
            .fixedSize(horizontal: false, vertical: true)

            Divider()
                .padding(.horizontal, 20)

            HStack {
                Text("Irrlicht v\(appVersion)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer()
                Button("Done") { isPresented = false }
                    .keyboardShortcut(.defaultAction)
            }
            .padding(.horizontal, 20)
            .padding(.top, 12)
            .padding(.bottom, 20)
        }
        .frame(width: 360)
        .background(Color(NSColor.windowBackgroundColor))
        .toggleStyle(IrrlichtSwitchToggleStyle())
        .background(
            WindowAccessor { window in
                // The panel is .borderless + .nonactivatingPanel + orderFrontRegardless(),
                // so canBecomeKey is false by default and the app is never active.
                // Activate + makeKey here so Settings text fields are editable.
                NSApp.activate(ignoringOtherApps: true)
                window.makeKey()
            }
        )
    }

    private var lastCheckedDescription: String {
        guard let date = updateManager.lastUpdateCheckDate else { return "Never" }
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .short
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    @ViewBuilder
    private func providerModeRow(label: String, selection: Binding<String>) -> some View {
        HStack {
            Text(label)
                .font(.callout)
                .frame(width: 80, alignment: .leading)
            Picker("", selection: selection) {
                ForEach(ProviderModePreference.allCases) { mode in
                    Text(mode.label).tag(mode.rawValue)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
        }
    }

    private func commitRelayURL() {
        relayServerURL = relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Reflect the login item's real system status (not just the stored
    /// preference) so the `.requiresApproval` hint can appear. An optional
    /// delay lets a detached register/unregister land first.
    private func refreshLoginItemStatus(after delay: TimeInterval = 0) {
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) {
            loginItemStatus = LoginItemManager.status
        }
    }

    private func checkNotificationAuth() {
        guard Bundle.main.bundleIdentifier != nil,
              Bundle.main.bundleURL.pathExtension == "app" else { return }
        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { settings in
            DispatchQueue.main.async {
                notificationsDenied = settings.authorizationStatus == .denied
            }
            let anyEnabled = NotificationEvent.allCases.contains {
                UserDefaults.standard.bool(forKey: $0.enabledKey)
            }
            if settings.authorizationStatus == .notDetermined, anyEnabled {
                DispatchQueue.main.async {
                    center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
                        DispatchQueue.main.async {
                            notificationsDenied = !granted
                        }
                    }
                }
            }
        }
    }
}

/// Settings subsection for putting the bundle-embedded irrlicht-ls CLI on
/// PATH (#608). Consent-first: the link is only created when the user clicks
/// the button — installers handle the automatic path; this covers DMG
/// drag-installs. Hidden in dev builds (binary not embedded).
private struct CLIToolSection: View {
    @State private var status: CLIToolInstaller.Status = .unavailable
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Command-Line Tool")
                .font(.subheadline)
                .fontWeight(.medium)

            switch status {
            case .unavailable:
                Text("irrlicht-ls is not embedded in this build.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            case .installed(let path):
                HStack(spacing: 4) {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundColor(.green)
                        .font(.caption)
                    Text("irrlicht-ls installed at \(path)")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            case .notInstalled:
                Text("Make the irrlicht-ls session list available in your terminal.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Button("Install Command-Line Tool") {
                    switch CLIToolInstaller.install() {
                    case .installed:
                        errorMessage = nil
                    case .failed(let message):
                        errorMessage = message
                    }
                    status = CLIToolInstaller.status()
                }
                .controlSize(.small)
                if let errorMessage {
                    Text(errorMessage)
                        .font(.caption)
                        .foregroundColor(.orange)
                        .textSelection(.enabled)
                }
            }
        }
        .onAppear { status = CLIToolInstaller.status() }
    }
}

private struct WindowAccessor: NSViewRepresentable {
    let onWindow: (NSWindow) -> Void
    func makeNSView(context: Context) -> NSView {
        let v = NSView()
        // Defer one runloop so NSView.window is populated (nil during makeNSView).
        DispatchQueue.main.async { [weak v] in
            if let w = v?.window { onWindow(w) }
        }
        return v
    }
    func updateNSView(_ nsView: NSView, context: Context) {}
}

/// The "Alert at [value] [% / tokens]" control under the context-pressure
/// notification row. One threshold, entered as a percentage of the context
/// window or as an absolute token count (issue #689).
private struct ContextThresholdRow: View {
    @AppStorage(ContextPressureThreshold.valueKey) private var value: Double = ContextPressureThreshold.defaultValue
    @AppStorage(ContextPressureThreshold.unitKey) private var unitRaw: String = ContextPressureThreshold.defaultUnit.rawValue
    let enabled: Bool

    @State private var draft: String = ""
    @FocusState private var fieldFocused: Bool

    private var unit: ContextPressureThreshold.Unit {
        ContextPressureThreshold.Unit(rawValue: unitRaw) ?? ContextPressureThreshold.defaultUnit
    }

    var body: some View {
        HStack(spacing: 8) {
            Text("Alert at")
                .font(.caption)
                .foregroundColor(.secondary)

            TextField("", text: $draft)
                .textFieldStyle(.roundedBorder)
                .multilineTextAlignment(.trailing)
                .font(.system(.caption, design: .monospaced))
                .frame(width: 72)
                .focused($fieldFocused)
                .onSubmit { commit() }
                .onChange(of: fieldFocused) { focused in
                    if !focused { commit() }
                }
                .tooltip(unit == .percent
                    ? "Alert when context reaches this % of the window"
                    : "Alert when the session reaches this many tokens")

            Picker("", selection: $unitRaw) {
                Text("%").tag(ContextPressureThreshold.Unit.percent.rawValue)
                Text("tokens").tag(ContextPressureThreshold.Unit.tokens.rawValue)
            }
            .labelsHidden()
            .frame(width: 92)
            .onChange(of: unitRaw) { newRaw in
                let newUnit = ContextPressureThreshold.Unit(rawValue: newRaw) ?? ContextPressureThreshold.defaultUnit
                value = ContextPressureThreshold.defaultValue(for: newUnit)
                draft = formatted(value)
            }

            Spacer()
        }
        .disabled(!enabled)
        .onAppear { draft = formatted(value) }
    }

    private func formatted(_ v: Double) -> String { "\(Int(v))" }

    /// Parse, clamp, and persist the draft, then reformat the field from the
    /// stored value so it never displays a number that wasn't saved (an empty
    /// or unparseable entry reverts to the current value).
    private func commit() {
        let trimmed = draft.trimmingCharacters(in: .whitespaces)
        if let parsed = Double(trimmed) {
            switch unit {
            case .percent: value = min(max(parsed, 1), 99)
            case .tokens: value = max(parsed, 1)
            }
        }
        draft = formatted(value)
    }
}

/// One row in the Settings notifications section: enable toggle, sound
/// picker, and a ▶ preview button.
private struct NotificationEventRow: View {
    let event: NotificationEvent
    @Binding var enabled: Bool
    let sampleText: String
    let onImportError: (String) -> Void

    @State private var selection: SoundChoice = .default

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            LeadingToggle(isOn: $enabled, label: event.displayName)
            HStack(spacing: 8) {
                Picker("", selection: $selection) {
                    ForEach(SoundChoice.builtIns, id: \.self) { choice in
                        Text(choice.displayName).tag(choice)
                    }
                    Divider()
                    Text("None").tag(SoundChoice.none)
                    ForEach(SoundChoice.speakChoices, id: \.self) { choice in
                        Text(choice.displayName).tag(choice)
                    }
                    if case .custom = selection {
                        Text(selection.displayName).tag(selection)
                    }
                    Divider()
                    Text("Custom audio file…").tag(SoundChoice.customPickerSentinel)
                }
                .labelsHidden()
                .disabled(!enabled)
                .frame(maxWidth: .infinity)
                .onChange(of: selection) { newValue in
                    handle(newValue)
                }

                Button {
                    SoundPlayer.preview(selection, sampleText: sampleText)
                } label: {
                    Image(systemName: "play.fill")
                        .frame(width: 14, height: 14)
                }
                .buttonStyle(.bordered)
                .disabled(!enabled || selection == .none)
                .tooltip("Preview")
            }

            if case .speak(let voice) = selection,
               voice != .default,
               !voice.isInstalled {
                Button {
                    Self.openSpokenContentSettings()
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "arrow.down.circle")
                        Text("Install \(voice.displayName) in System Settings")
                    }
                    .font(.caption)
                }
                .buttonStyle(.link)
                .foregroundColor(.orange)
                .tooltip("Open System Settings → Accessibility → Spoken Content")
            }
        }
        .onAppear { loadFromDefaults() }
    }

    private static func openSpokenContentSettings() {
        let candidates = [
            "x-apple.systempreferences:com.apple.Accessibility-Settings.extension?SpokenContent",
            "x-apple.systempreferences:com.apple.preference.universalaccess",
        ]
        for raw in candidates {
            if let url = URL(string: raw), NSWorkspace.shared.open(url) { return }
        }
    }

    private func loadFromDefaults() {
        let raw = UserDefaults.standard.string(forKey: event.soundKey) ?? event.defaultSound.rawValue
        selection = SoundChoice(rawValue: raw) ?? event.defaultSound
    }

    private func handle(_ newValue: SoundChoice) {
        if newValue == .customPickerSentinel {
            let previous = SoundChoice(rawValue: UserDefaults.standard.string(forKey: event.soundKey) ?? "") ?? .default
            selection = previous
            pickCustomFile()
            return
        }
        UserDefaults.standard.set(newValue.rawValue, forKey: event.soundKey)
    }

    private func pickCustomFile() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowedContentTypes = [UTType.audio]
        panel.message = "Choose an audio file (aiff, wav, mp3, m4a, caf)"
        let response = panel.runModal()
        guard response == .OK, let url = panel.url else { return }

        SoundPlayer.installCustom(srcURL: url, event: event) { result in
            switch result {
            case .success(let installed):
                let choice = SoundChoice.custom(installedFilename: installed, displayPath: url.path)
                UserDefaults.standard.set(choice.rawValue, forKey: event.soundKey)
                selection = choice
                onImportError("")
            case .failure(let error):
                onImportError("Could not import audio file: \(error.localizedDescription)")
            }
        }
    }
}

/// Left-aligned switch + label, rendered by `IrrlichtSwitchToggleStyle`.
/// An optional `info` string adds a hover-reveal ⓘ next to the label so the
/// explanation doesn't cost a permanent block of caption text below the row.
/// Internal (not private): PermissionWizardView reuses it.
struct LeadingToggle: View {
    @Binding var isOn: Bool
    let label: String
    var info: String? = nil
    /// Flags a still-maturing feature with a "BETA" pill after the label (#694).
    var beta: Bool = false

    var body: some View {
        HStack(spacing: 6) {
            Toggle(isOn: $isOn) { Text(label) }
                .fixedSize()
            if let info {
                InfoIcon(text: info)
            }
            if beta {
                BetaBadge()
            }
            Spacer()
        }
    }
}

/// Small "BETA" pill flagging a feature that's still maturing (#694). Mirrors
/// the web dashboard's `.beta-badge`.
struct BetaBadge: View {
    var body: some View {
        Text("BETA")
            .font(.system(size: 9, weight: .bold))
            .tracking(0.5)
            .foregroundColor(IrrColors.working)
            .padding(.horizontal, 5)
            .padding(.vertical, 1)
            .background(Capsule().fill(IrrColors.workingDim))
            .accessibilityLabel("Beta feature")
    }
}

/// Small ⓘ affordance: reveals a one-line explainer on hover instead of
/// spending vertical space on a caption paragraph under every control.
/// Internal (not private): PermissionWizardView reuses it.
struct InfoIcon: View {
    let text: String

    var body: some View {
        Image(systemName: "info.circle")
            .font(.caption)
            .foregroundColor(.secondary)
            .tooltip(text)
            .accessibilityLabel(text)
    }
}

/// Custom switch style: a green-on / neutral-off capsule with a sliding
/// white knob. Replaces `ToggleStyle.switch` because macOS's NSSwitch-backed
/// switch ignores `.tint(_:)` — its on color is locked to the system accent.
/// Drawing the pill ourselves makes the color independent of System Settings.
/// Internal (not private): PermissionWizardView applies it too.
struct IrrlichtSwitchToggleStyle: ToggleStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 8) {
            ZStack(alignment: configuration.isOn ? .trailing : .leading) {
                Capsule()
                    .fill(configuration.isOn ? IrrColors.ready : IrrColors.cancelled.opacity(0.4))
                Circle()
                    .fill(Color.white)
                    .shadow(color: Color.black.opacity(0.18), radius: 1, x: 0, y: 0.5)
                    .padding(2)
            }
            .frame(width: 30, height: 18)
            .animation(IrrMotion.easeOut(duration: IrrMotion.fast), value: configuration.isOn)

            configuration.label
        }
        .contentShape(Rectangle())
        .onTapGesture { configuration.isOn.toggle() }
        .accessibilityAddTraits(.isButton)
        .accessibilityValue(configuration.isOn ? "on" : "off")
    }
}

private extension SoundChoice {
    static let customPickerSentinel: SoundChoice = .custom(installedFilename: "__picker_sentinel__", displayPath: "")
}
