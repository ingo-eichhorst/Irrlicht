import SwiftUI

struct SettingsView: View {
    @Binding var isPresented: Bool
    @AppStorage("sessionTTLMinutes") private var sessionTTLMinutes: Int = 30

    private let ttlOptions: [(label: String, value: Int)] = [
        ("Never", 0),
        ("15 minutes", 15),
        ("30 minutes", 30),
        ("1 hour", 60),
        ("4 hours", 240),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Settings")
                .font(.headline)

            Divider()

            VStack(alignment: .leading, spacing: 8) {
                Text("Session Expiry")
                    .font(.subheadline)
                    .foregroundColor(.primary)

                Text("Auto-delete finished sessions after this idle time.")
                    .font(.caption)
                    .foregroundColor(.secondary)

                Picker("Expire after", selection: $sessionTTLMinutes) {
                    ForEach(ttlOptions, id: \.value) { option in
                        Text(option.label).tag(option.value)
                    }
                }
                .pickerStyle(.menu)
                .frame(maxWidth: 200)
            }

            Spacer()

            HStack {
                Spacer()
                Button("Done") { isPresented = false }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 320, height: 180)
    }
}
