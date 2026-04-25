import SwiftUI
import UserNotifications

struct SettingsView: View {
    @Binding var isPresented: Bool
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("notifyOnReady") private var notifyOnReady: Bool = false
    @AppStorage("notifyOnWaiting") private var notifyOnWaiting: Bool = false
    @State private var notificationsDenied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Settings")
                .font(.headline)

            Divider()

            VStack(alignment: .leading, spacing: 8) {
                Toggle("Debug Mode", isOn: $debugMode)

                Text("Show session IDs, creation time, and time since last update.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            VStack(alignment: .leading, spacing: 8) {
                Toggle("Show Estimated Cost", isOn: $showCostDisplay)

                Text("Display estimated USD cost per session and per project group. Cost estimates are approximate.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            Divider()

            VStack(alignment: .leading, spacing: 8) {
                Text("Notifications")
                    .font(.subheadline)
                    .fontWeight(.medium)

                Toggle("Notify when agent is ready", isOn: $notifyOnReady)

                Toggle("Notify when agent is waiting", isOn: $notifyOnWaiting)

                Text("Send a desktop notification when an agent finishes work or needs your input.")
                    .font(.caption)
                    .foregroundColor(.secondary)

                if notificationsDenied && (notifyOnReady || notifyOnWaiting) {
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

            Spacer()

            HStack {
                Spacer()
                Button("Done") { isPresented = false }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 320, height: 380)
        .background(Color(NSColor.windowBackgroundColor))
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
                UserDefaults.standard.bool(forKey: "notifyOnWaiting")) {
                DispatchQueue.main.async {
                    // Request authorization — the SessionManager startup flow handles
                    // the LSUIElement workaround, but if it hasn't run yet we try here too.
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
