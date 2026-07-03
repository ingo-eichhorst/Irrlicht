import Foundation

// MARK: - Session Management Actions
//
// Split out of SessionManager.swift (#807): user-initiated mutations against
// the daemon's on-disk session files (reset to ready, delete), plus the
// shared teardown of a session's in-memory state.

extension SessionManager {
    func resetSessionState(sessionId: String) {
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        do {
            guard let existingData = try? Data(contentsOf: sessionFilePath),
                  let existingJson = try? JSONSerialization.jsonObject(with: existingData) as? [String: Any] else {
                lastError = "Failed to load session data for reset"
                return
            }
            var updatedJson = existingJson
            updatedJson["state"] = "ready"
            updatedJson["updated_at"] = Int64(Date().timeIntervalSince1970)
            let updatedData = try JSONSerialization.data(withJSONObject: updatedJson, options: [])
            try updatedData.write(to: sessionFilePath)
            if let s = sessionMap[sessionId]?.withState(.ready) {
                sessionMap[sessionId] = s
                rebuildSessionsFromMap()
                patchApiGroups(session: s)
            }
        } catch {
            lastError = "Failed to reset session: \(error.localizedDescription)"
        }
    }

    /// `scheduleRehydration` is intentionally not called — we're authoritative
    /// for the local delete, and a rehydrate could re-add the row before the
    /// daemon's file watcher catches up.
    func deleteSession(sessionId: String) {
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        if FileManager.default.fileExists(atPath: sessionFilePath.path) {
            do {
                try FileManager.default.removeItem(at: sessionFilePath)
            } catch {
                lastError = "Failed to delete session: \(error.localizedDescription)"
                return
            }
        }
        notifiedThresholds.removeValue(forKey: sessionId)
        purgeSessionState(sessionId: sessionId)
        saveSessionOrder()
    }

    /// Caller persists `sessionOrder` if it's authoritative for the deletion;
    /// the WS handler skips the save because the daemon is the source of truth
    /// in that path and will re-emit on reconnect.
    func purgeSessionState(sessionId: String) {
        sessionMap.removeValue(forKey: sessionId)
        sessionOrder.removeAll { $0 == sessionId }
        lastTickGen.removeValue(forKey: sessionId)
        for gran in [1, 10, 60] {
            historyByGranularity[gran]?.removeValue(forKey: sessionId)
        }
        rebuildSessionsFromMap()
        removeFromApiGroups(sessionId: sessionId)
    }
}
