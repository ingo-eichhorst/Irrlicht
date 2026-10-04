import Foundation
import SwiftUI
import SystemConfiguration

/// One field that joins this Mac to a relay from an enrollment URL (#1965):
/// paste `https://<relay>/enroll/XXXX-XXXX` (or a bare code, sent to the
/// relay already configured), press Join, and the relay origin and redeemed
/// token are handed to `onEnrolled`. Parsing and every failure message live
/// in `RelayEnrollmentClient`.
struct RelayEnrollmentField: View {
    /// Where a bare code is redeemed; ignored for a full enrollment URL.
    let configuredRelayURL: String
    /// Called on the main actor with the relay origin and the redeemed token.
    let onEnrolled: (URL, String) throws -> Void

    private enum Phase: Equatable {
        case idle
        case redeeming
        case joined(String)
        case failed(String)
    }

    @State private var draft: String = ""
    @State private var phase: Phase = .idle

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                TextField("Enrollment URL (https://relay.example.com/enroll/…)", text: $draft)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(.caption, design: .monospaced))
                    .autocorrectionDisabled(true)
                    .onSubmit { join() }
                Button("Join") { join() }
                    .disabled(phase == .redeeming || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }

            switch phase {
            case .idle:
                Text("Paste the URL printed by `irrlichtrelay enroll new` to connect and publish in one step.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            case .redeeming:
                ProgressView("Joining relay…")
                    .controlSize(.small)
            case .joined(let host):
                Text("Joined \(host). Publishing is on.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            case .failed(let message):
                Text(message)
                    .font(.caption)
                    .foregroundColor(.red)
            }
        }
        .onChange(of: draft) { _ in
            if case .failed = phase { phase = .idle }
        }
        // No onDisappear cancel: the panel hides on any click elsewhere, and
        // once the POST is out the relay may already have spent the one-time
        // code, so a redeem always runs to completion and stores its token.
    }

    private func join() {
        guard phase != .redeeming else { return }
        guard let parsed = RelayEnrollmentClient.parse(draft, configuredRelayURL: configuredRelayURL) else {
            phase = .failed("Paste the full enrollment URL, like https://relay.example.com/enroll/K7QM-3PXA, or set an https/wss relay URL below before pasting a bare code.")
            return
        }
        phase = .redeeming
        // The computer name from System Settings; unlike Host.current() it
        // does no hostname resolution on the main actor.
        let label = SCDynamicStoreCopyComputerName(nil, nil) as String? ?? ""
        Task {
            do {
                let token = try await RelayEnrollmentClient.redeem(
                    origin: parsed.origin, code: parsed.code, label: label)
                try await MainActor.run {
                    try onEnrolled(parsed.origin, token)
                    draft = ""
                    phase = .joined(parsed.origin.host ?? parsed.origin.absoluteString)
                }
            } catch {
                await MainActor.run { phase = .failed(error.localizedDescription) }
            }
        }
    }
}
