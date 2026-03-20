import SwiftUI

// MARK: - ConfigurationActionView

/// Shown in the empty-state slot when no sessions are active.
///
/// Detects the installation status of Claude Code and the irrlicht-hook
/// integration and guides the user through any missing setup steps.
///
/// Detection priority (top wins):
///  1. Claude Code not installed          → install link
///  2. irrlicht-hook binary missing       → reinstall guide
///  3. Hooks not configured               → "Configure Hooks Now" button
///  4. Hooks partially configured         → missing-event list + update button
///  5. Fully configured / no sessions yet → idle waiting message
struct ConfigurationActionView: View {
    @State private var claudeStatus: ClaudeCodeStatus = .installed
    @State private var hookStatus: HookStatus = .fullyConfigured
    @State private var isRunningMerge = false
    @State private var mergeMessage: String? = nil

    var body: some View {
        VStack(spacing: 12) {
            stateContent
        }
        .frame(maxWidth: .infinity)
        .padding(20)
        .onAppear { refreshStatus() }
    }

    // MARK: - State Content

    @ViewBuilder
    private var stateContent: some View {
        switch (claudeStatus, hookStatus) {
        case (.notInstalled, _):
            claudeNotInstalledView
        case (.installed, .binaryMissing):
            binaryMissingView
        case (.installed, .notConfigured):
            notConfiguredView
        case (.installed, .partiallyConfigured(let missing)):
            partiallyConfiguredView(missing: missing)
        default:
            idleView
        }
    }

    // MARK: - Claude Code Not Installed

    private var claudeNotInstalledView: some View {
        VStack(spacing: 8) {
            Image(systemName: "xmark.circle")
                .font(.system(size: 24))
                .foregroundColor(.red)

            Text("Claude Code not installed")
                .font(.headline)
                .foregroundColor(.primary)

            Text("Install the Claude Code CLI to use Irrlicht.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)

            Link("Install Claude Code →",
                 destination: URL(string: "https://claude.ai/download")!)
                .font(.caption)
                .foregroundColor(.accentColor)
        }
    }

    // MARK: - Hook Binary Missing

    private var binaryMissingView: some View {
        VStack(spacing: 8) {
            Image(systemName: "wrench.and.screwdriver")
                .font(.system(size: 24))
                .foregroundColor(.orange)

            Text("irrlicht-hook not found")
                .font(.headline)
                .foregroundColor(.primary)

            Text("The irrlicht-hook binary is missing from your PATH.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)

            Text("Reinstall Irrlicht or copy the binary to /usr/local/bin.")
                .font(.caption2)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)

            Button("Open Irrlicht Releases") {
                NSWorkspace.shared.open(
                    URL(string: "https://github.com/ingo-eichhorst/Irrlicht/releases")!
                )
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)
        }
    }

    // MARK: - Hooks Not Configured

    private var notConfiguredView: some View {
        VStack(spacing: 8) {
            Image(systemName: "gear.badge.xmark")
                .font(.system(size: 24))
                .foregroundColor(.orange)

            Text("Hooks not configured")
                .font(.headline)
                .foregroundColor(.primary)

            Text("irrlicht-hook is installed but not wired into Claude Code.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)

            mergeButton(label: "Configure Hooks Now")

            feedbackLabel
        }
    }

    // MARK: - Partially Configured

    private func partiallyConfiguredView(missing: [String]) -> some View {
        VStack(spacing: 8) {
            Image(systemName: "gear.badge.questionmark")
                .font(.system(size: 24))
                .foregroundColor(.yellow)

            Text("Hooks partially configured")
                .font(.headline)
                .foregroundColor(.primary)

            Text("The following hook events are not wired:")
                .font(.caption)
                .foregroundColor(.secondary)

            VStack(alignment: .leading, spacing: 2) {
                ForEach(missing, id: \.self) { event in
                    HStack(spacing: 4) {
                        Image(systemName: "minus.circle.fill")
                            .font(.caption2)
                            .foregroundColor(.red)
                        Text(event)
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                }
            }
            .padding(.vertical, 4)

            mergeButton(label: "Update Configuration")

            feedbackLabel
        }
    }

    // MARK: - Idle (fully configured, no sessions)

    private var idleView: some View {
        VStack(spacing: 8) {
            Image(systemName: "lightbulb.slash")
                .font(.system(size: 24))
                .foregroundColor(.secondary)

            Text("No Claude Code sessions detected")
                .font(.headline)
                .foregroundColor(.secondary)

            Text("Start a Claude Code session to see it here.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
        }
    }

    // MARK: - Shared Subviews

    private func mergeButton(label: String) -> some View {
        Button(action: runSettingsMerger) {
            if isRunningMerge {
                ProgressView()
                    .controlSize(.small)
            } else {
                Text(label)
            }
        }
        .buttonStyle(.borderedProminent)
        .controlSize(.small)
        .disabled(isRunningMerge)
    }

    @ViewBuilder
    private var feedbackLabel: some View {
        if let msg = mergeMessage {
            Text(msg)
                .font(.caption2)
                .foregroundColor(msg.hasPrefix("Error") ? .red : .green)
                .multilineTextAlignment(.center)
        }
    }

    // MARK: - Actions

    private func refreshStatus() {
        claudeStatus = SystemStatusDetector.detectClaudeCodeStatus()
        hookStatus = SystemStatusDetector.detectHookStatus()
    }

    private func runSettingsMerger() {
        isRunningMerge = true
        mergeMessage = nil

        DispatchQueue.global(qos: .userInitiated).async {
            let binaryPath = resolvedBinaryPath("irrlicht-settings-merger")
            let task = Process()
            task.executableURL = URL(fileURLWithPath: binaryPath ?? "/usr/local/bin/irrlicht-settings-merger")
            task.arguments = ["--action", "merge"]
            let pipe = Pipe()
            task.standardOutput = pipe
            task.standardError = pipe

            do {
                try task.run()
                task.waitUntilExit()
                let output = String(
                    data: pipe.fileHandleForReading.readDataToEndOfFile(),
                    encoding: .utf8
                ) ?? ""
                DispatchQueue.main.async {
                    isRunningMerge = false
                    if task.terminationStatus == 0 {
                        mergeMessage = "Hooks configured successfully."
                    } else {
                        mergeMessage = "Error: \(output.trimmingCharacters(in: .whitespacesAndNewlines))"
                    }
                    refreshStatus()
                }
            } catch {
                DispatchQueue.main.async {
                    isRunningMerge = false
                    mergeMessage = "Error: \(error.localizedDescription)"
                }
            }
        }
    }

    /// Resolves a binary path using `which`. Returns `nil` if not found.
    private func resolvedBinaryPath(_ name: String) -> String? {
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        task.arguments = [name]
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = FileHandle.nullDevice
        do {
            try task.run()
            task.waitUntilExit()
            guard task.terminationStatus == 0 else { return nil }
            let path = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            return path?.isEmpty == false ? path : nil
        } catch {
            return nil
        }
    }
}

// MARK: - Preview

struct ConfigurationActionView_Previews: PreviewProvider {
    static var previews: some View {
        Group {
            ConfigurationActionView()
                .previewDisplayName("Live")
        }
        .frame(width: 350)
        .background(Color(NSColor.windowBackgroundColor))
    }
}
