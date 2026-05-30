import SwiftUI
import UniformTypeIdentifiers
import UserNotifications

struct SettingsView: View {
    @Binding var isPresented: Bool
    @EnvironmentObject var updateManager: UpdateManager
    @EnvironmentObject var sessionManager: SessionManager
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("showQuotaForecast") private var showQuotaForecast: Bool = true
    @AppStorage("launchAtLogin") private var launchAtLogin: Bool = true
    @AppStorage("providerMode_anthropic") private var providerModeAnthropic: String = ProviderModePreference.auto.rawValue
    @AppStorage("providerMode_openai") private var providerModeOpenAI: String = ProviderModePreference.auto.rawValue
    @AppStorage(NotificationEvent.ready.enabledKey) private var notifyOnReady: Bool = true
    @AppStorage(NotificationEvent.waiting.enabledKey) private var notifyOnWaiting: Bool = true
    @AppStorage(NotificationEvent.contextPressure.enabledKey) private var notifyOnContextPressure: Bool = true
    // Sources (multi-source): mirror the web dashboard's keys.
    @AppStorage("useLocalDaemon") private var useLocalDaemon: Bool = true
    @AppStorage("useRelayServer") private var useRelayServer: Bool = false
    @AppStorage("relayServerURL") private var relayServerURL: String = ""
    @State private var relayURLDraft: String = ""
    @State private var relayURLDebounceTask: Task<Void, Never>? = nil
    // Relay bearer token lives in the Keychain (not @AppStorage); this draft
    // mirrors it for the field, loaded on appear and written on change.
    @State private var relayTokenDraft: String = ""
    @State private var notificationsDenied = false
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
                        isOn: $debugMode,
                        label: "Debug Mode",
                        info: "Show session IDs, creation time, and time since last update."
                    )

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

                    LeadingToggle(
                        isOn: $launchAtLogin,
                        label: "Open at Login",
                        info: "Start Irrlicht automatically when you log in to your Mac."
                    )
                    .onChange(of: launchAtLogin) { newValue in LoginItemManager.setEnabled(newValue) }

                    Divider()

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Sources")
                            .font(.subheadline)
                            .fontWeight(.medium)

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

                        if useRelayServer {
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
                                        }
                                    }
                                Circle()
                                    .fill(sessionManager.relayConnectionState.dotColor)
                                    .frame(width: 8, height: 8)
                                    .help(sessionManager.relayConnectionState.shortLabel)
                            }
                            SecureField("Relay token (leave empty if the relay has no auth)", text: $relayTokenDraft)
                                .textFieldStyle(.roundedBorder)
                                .font(.system(.caption, design: .monospaced))
                                .autocorrectionDisabled(true)
                                .onChange(of: relayTokenDraft) { newValue in
                                    KeychainStore.set(newValue.trimmingCharacters(in: .whitespacesAndNewlines), account: "relayToken")
                                    // Keychain writes don't fire UserDefaults.didChangeNotification,
                                    // so nudge the relay to reconnect with the new token.
                                    sessionManager.relayTokenDidChange()
                                }
                        }
                    }
                    .onAppear {
                        relayURLDraft = relayServerURL
                        relayTokenDraft = KeychainStore.get(account: "relayToken")
                    }
                    .onChange(of: useRelayServer) { on in
                        if on && relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                            relayURLDraft = "ws://localhost:7839"
                        }
                        commitRelayURL()
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
private struct LeadingToggle: View {
    @Binding var isOn: Bool
    let label: String
    var info: String? = nil

    var body: some View {
        HStack(spacing: 6) {
            Toggle(isOn: $isOn) { Text(label) }
                .fixedSize()
            if let info {
                InfoIcon(text: info)
            }
            Spacer()
        }
    }
}

/// Small ⓘ affordance: reveals a one-line explainer on hover instead of
/// spending vertical space on a caption paragraph under every control.
private struct InfoIcon: View {
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
private struct IrrlichtSwitchToggleStyle: ToggleStyle {
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
