import Foundation

// MARK: - Debug State Dump (IRRLICHT_DEBUG=1)
//
// Split out of SessionManager.swift (#807): the debug-state.json writer used
// by agents/humans to verify UI state without attaching a debugger.

extension SessionManager {
    var isDebugMode: Bool { irrlichtVerboseLogging }

    /// Writes current session state to ~/.irrlicht/debug-state.json when IRRLICHT_DEBUG=1.
    /// Agents can verify UI state with: cat ~/.irrlicht/debug-state.json
    func writeDebugState() {
        guard isDebugMode else { return }

        let debugDir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".irrlicht")
        let debugFile = debugDir.appendingPathComponent("debug-state.json")

        do {
            try FileManager.default.createDirectory(at: debugDir, withIntermediateDirectories: true)

            let entries = sessions.map { session in
                DebugSessionEntry(
                    id: session.id,
                    projectName: session.projectName,
                    state: session.state.rawValue,
                    model: session.effectiveModel,
                    contextUtilization: session.metrics?.contextUtilization ?? 0.0,
                    totalTokens: session.metrics?.totalTokens ?? 0
                )
            }

            // Compute project groups (mirrors SessionListView.projectGroups logic)
            let sessionIds = Set(sessions.map { $0.id })
            let subagentIds: Set<String> = Set(sessions.compactMap { session in
                guard let pid = session.parentSessionId, sessionIds.contains(pid) else { return nil }
                return session.id
            })
            var grouped: [String: [String]] = [:]
            for session in sessions where !subagentIds.contains(session.id) {
                let cwd = session.cwd
                let projectDir: String
                if cwd.isEmpty {
                    projectDir = "unknown"
                } else {
                    let last = URL(fileURLWithPath: cwd).lastPathComponent
                    projectDir = last.isEmpty ? "unknown" : last
                }
                grouped[projectDir, default: []].append(session.id)
            }
            let projectGroups = grouped.map { key, sessionGroupIds in
                DebugProjectGroup(
                    projectDirectory: key,
                    accessibilityIdentifier: "project-group-\(key)",
                    sessionIds: sessionGroupIds.sorted()
                )
            }.sorted { $0.projectDirectory < $1.projectDirectory }

            let formatter = ISO8601DateFormatter()
            let debugState = DebugState(
                sessions: entries,
                projectGroups: projectGroups,
                sessionCount: sessions.count,
                workingCount: workingSessions,
                waitingCount: waitingSessions,
                readyCount: readySessions,
                lastUpdated: formatter.string(from: Date())
            )

            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let data = try encoder.encode(debugState)
            try data.write(to: debugFile, options: .atomic)
        } catch {
            print("⚠️ Failed to write debug state: \(error.localizedDescription)")
        }
    }
}

private struct DebugSessionEntry: Codable {
    let id: String
    let projectName: String?
    let state: String
    let model: String
    let contextUtilization: Double
    let totalTokens: Int64
}

private struct DebugProjectGroup: Codable {
    let projectDirectory: String
    let accessibilityIdentifier: String
    let sessionIds: [String]
}

private struct DebugState: Codable {
    let sessions: [DebugSessionEntry]
    let projectGroups: [DebugProjectGroup]
    let sessionCount: Int
    let workingCount: Int
    let waitingCount: Int
    let readyCount: Int
    let lastUpdated: String
}
