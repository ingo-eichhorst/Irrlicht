import SwiftUI
import UniformTypeIdentifiers
import UserNotifications

struct SettingsView: View {
    @Binding var isPresented: Bool
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("notifyOnReady") private var notifyOnReady: Bool = true
    @AppStorage("notifyOnWaiting") private var notifyOnWaiting: Bool = true
    @AppStorage("notifyOnContextPressure") private var notifyOnContextPressure: Bool = true
    @State private var notificationsDenied = false
    @State private var customImportError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Settings")
                .font(.headline)

            Divider()

            VStack(alignment: .leading, spacing: 8) {
                LeadingToggle(isOn: $debugMode, label: "Debug Mode")

                Text("Show session IDs, creation time, and time since last update.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            VStack(alignment: .leading, spacing: 8) {
                LeadingToggle(isOn: $showCostDisplay, label: "Show Estimated Cost")

                Text("Display estimated USD cost per session and per project group. Cost estimates are approximate.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Divider()

            VStack(alignment: .leading, spacing: 10) {
                Text("Notifications")
                    .font(.subheadline)
                    .fontWeight(.medium)

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

                Text("Pick a sound per event, choose your own audio file, or have the message read aloud.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

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

            Spacer()

            HStack {
                Spacer()
                Button("Done") { isPresented = false }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 360, height: 480)
        .background(Color(NSColor.windowBackgroundColor))
        .toggleStyle(IrrlichtSwitchToggleStyle())
    }

    private func checkNotificationAuth() {
        guard Bundle.main.bundleIdentifier != nil,
              Bundle.main.bundleURL.pathExtension == "app" else { return }
        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { settings in
            DispatchQueue.main.async {
                notificationsDenied = settings.authorizationStatus == .denied
            }
            // If user just toggled a notification setting and we've never asked,
            // show the "blocked" banner so the user knows to enable in System Settings.
            if settings.authorizationStatus == .notDetermined,
               (UserDefaults.standard.bool(forKey: "notifyOnReady") ||
                UserDefaults.standard.bool(forKey: "notifyOnWaiting") ||
                UserDefaults.standard.bool(forKey: "notifyOnContextPressure")) {
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
                    Text("Speak aloud").tag(SoundChoice.speak)
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
        }
        .onAppear { loadFromDefaults() }
    }

    private func loadFromDefaults() {
        let raw = UserDefaults.standard.string(forKey: event.soundKey) ?? event.defaultSound.rawValue
        selection = SoundChoice(rawValue: raw) ?? event.defaultSound
    }

    private func handle(_ newValue: SoundChoice) {
        if newValue == .customPickerSentinel {
            // Restore previous selection until the open panel resolves.
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
private struct LeadingToggle: View {
    @Binding var isOn: Bool
    let label: String

    var body: some View {
        HStack {
            Toggle(isOn: $isOn) { Text(label) }
            Spacer()
        }
    }
}

/// Custom switch style: a green-on / neutral-off capsule with a sliding
/// white knob. Replaces `ToggleStyle.switch` because macOS's NSSwitch-backed
/// switch ignores `.tint(_:)` — its on color is locked to the system accent.
/// Drawing the pill ourselves makes the color independent of System Settings.
private struct IrrlichtSwitchToggleStyle: ToggleStyle {
    /// Same green that HistoryBarView uses for the "ready" state.
    private static let onColor = Color(hex: "#34C759")

    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 8) {
            ZStack(alignment: configuration.isOn ? .trailing : .leading) {
                Capsule()
                    .fill(configuration.isOn ? Self.onColor : Color.secondary.opacity(0.35))
                Circle()
                    .fill(Color.white)
                    .shadow(color: Color.black.opacity(0.18), radius: 1, x: 0, y: 0.5)
                    .padding(2)
            }
            .frame(width: 30, height: 18)
            .animation(.easeInOut(duration: 0.15), value: configuration.isOn)
            .onTapGesture { configuration.isOn.toggle() }
            .accessibilityAddTraits(.isButton)
            .accessibilityValue(configuration.isOn ? "on" : "off")

            configuration.label
        }
    }
}

private extension SoundChoice {
    /// Stand-in tag for the "Custom audio file…" picker item. We can't use
    /// `.custom(...)` directly because the menu row pre-exists the user's
    /// selection — picking it presents `NSOpenPanel` and only then promotes
    /// into a real `.custom` value.
    static let customPickerSentinel: SoundChoice = .custom(installedFilename: "__picker_sentinel__", displayPath: "")
}
