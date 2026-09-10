import SwiftUI

struct ElfdansPairingRequestGate {
    private var current = UUID()

    mutating func begin() -> UUID {
        current = UUID()
        return current
    }

    mutating func invalidate() {
        current = UUID()
    }

    func isCurrent(_ request: UUID) -> Bool {
        request == current
    }
}

struct ElfdansPairingView: View {
    let relayURL: String
    let relayToken: String

    private enum Phase: Equatable {
        case idle
        case loading
        case ready(ElfdansPairing)
        case failed(String)
    }

    @State private var phase: Phase = .idle
    @State private var requestGate = ElfdansPairingRequestGate()
    @State private var mintTask: Task<Void, Never>?

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack(spacing: IrrSpacing.sp2) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Pair a phone")
                        .font(.subheadline)
                        .fontWeight(.medium)
                    Text("Create a single-use code that expires after 10 minutes.")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                Spacer()
                Button(buttonTitle) { mint() }
                    .disabled(phase == .loading || relayURL.isEmpty || relayToken.isEmpty)
            }

            switch phase {
            case .idle:
                if relayURL.isEmpty || relayToken.isEmpty {
                    Text("Enter the relay URL and token to create a pairing code.")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            case .loading:
                ProgressView("Creating pairing code…")
                    .controlSize(.small)
            case .ready(let pairing):
                ElfdansPairingDetails(pairing: pairing)
            case .failed(let message):
                Text(message)
                    .font(.caption)
                    .foregroundColor(.red)
            }
        }
        .onChange(of: relayURL) { _ in reset() }
        .onChange(of: relayToken) { _ in reset() }
        .onDisappear { reset() }
    }

    private var buttonTitle: String {
        switch phase {
        case .ready, .failed: return "Create new code"
        default: return "Pair a phone…"
        }
    }

    private func mint() {
        mintTask?.cancel()
        let request = requestGate.begin()
        let requestedRelayURL = relayURL
        let requestedRelayToken = relayToken
        phase = .loading
        mintTask = Task {
            do {
                let pairing = try await ElfdansPairingClient.mint(
                    relayURL: requestedRelayURL,
                    token: requestedRelayToken
                )
                guard requestGate.isCurrent(request) else { return }
                phase = .ready(pairing)
                mintTask = nil
            } catch {
                guard requestGate.isCurrent(request) else { return }
                phase = .failed(error.localizedDescription)
                mintTask = nil
            }
        }
    }

    private func reset() {
        requestGate.invalidate()
        mintTask?.cancel()
        mintTask = nil
        phase = .idle
    }
}

struct ElfdansPairingDetails: View {
    let pairing: ElfdansPairing

    var body: some View {
        VStack(alignment: .center, spacing: IrrSpacing.sp2) {
            if let image = pairing.qrImage, let pairingURL = pairing.pairingURL {
                Image(nsImage: image)
                    .interpolation(.none)
                    .resizable()
                    .frame(width: 220, height: 220)
                    .background(Color.white)
                    .accessibilityLabel("QR code for \(pairingURL)")
            }
            Text(pairing.code)
                .font(.system(.title3, design: .monospaced).weight(.semibold))
                .tracking(2)
            if let pairingURL = pairing.pairingURL {
                Text(pairingURL)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.secondary)
                    .textSelection(.enabled)
                    .multilineTextAlignment(.center)
            }
            if let reason = pairing.pairingURLReason {
                Text(reason)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.leading)
            }
        }
        .frame(maxWidth: .infinity)
    }
}
