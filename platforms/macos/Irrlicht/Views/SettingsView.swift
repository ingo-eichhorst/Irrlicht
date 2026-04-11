import SwiftUI

struct SettingsView: View {
    @Binding var isPresented: Bool
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false

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

            Spacer()

            HStack {
                Spacer()
                Button("Done") { isPresented = false }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 320, height: 260)
    }
}
