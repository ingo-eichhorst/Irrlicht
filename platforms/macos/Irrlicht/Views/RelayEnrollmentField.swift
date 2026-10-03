import Foundation
import SwiftUI

/// One field that joins this Mac to a relay from an enrollment URL (#1965):
/// paste `https://<relay>/enroll/XXXX-XXXX`, press Join, and the redeemed
/// token, relay URL and publish toggle are handed to `onEnrolled`. Parsing
/// and every failure message live in `RelayEnrollmentClient`.
struct RelayEnrollmentField: View {
    /// Called on the main actor with the relay origin and the redeemed token.
    let onEnrolled: (URL, String) -> Void

    private enum Phase: Equatable {
        case idle
        case redeeming
        case joined(String)
        case failed(String)
    }

    @State private var draft: String = ""
    @State private var phase: Phase = .idle
    @State private var redeemTask: Task<Void, Never>?

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
        .onDisappear {
            redeemTask?.cancel()
            redeemTask = nil
            if phase == .redeeming { phase = .idle }
        }
    }

    private func join() {
        guard phase != .redeeming else { return }
        guard let parsed = RelayEnrollmentClient.parse(draft) else {
            phase = .failed("Paste the full enrollment URL, like https://relay.example.com/enroll/K7QM-3PXA.")
            return
        }
        phase = .redeeming
        let label = Host.current().localizedName ?? ""
        redeemTask = Task {
            do {
                let token = try await RelayEnrollmentClient.redeem(
                    origin: parsed.origin, code: parsed.code, label: label)
                await MainActor.run {
                    guard !Task.isCancelled else { return }
                    onEnrolled(parsed.origin, token)
                    draft = ""
                    phase = .joined(parsed.origin.host ?? parsed.origin.absoluteString)
                }
            } catch {
                await MainActor.run {
                    guard !Task.isCancelled else { return }
                    phase = .failed(error.localizedDescription)
                }
            }
        }
    }
}
