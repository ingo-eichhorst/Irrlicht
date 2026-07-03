import Foundation

// MARK: - Session order + duplicate-session handling
//
// Split out of SessionManager.swift (#807): the user's drag-to-reorder
// session order (persisted to disk) and duplicate-index assignment for
// sessions that share a project/branch.

extension SessionManager {
    // MARK: - Session Order Management

    func loadSessionOrder() {
        do {
            guard FileManager.default.fileExists(atPath: orderFilePath.path) else {
                print("📋 No session order file found, starting with empty order")
                return
            }

            let data = try Data(contentsOf: orderFilePath)
            let orderData = try JSONDecoder().decode(SessionOrderData.self, from: data)
            sessionOrder = orderData.order
            print("📋 Loaded session order with \(sessionOrder.count) sessions")
        } catch {
            print("📋 Failed to load session order: \(error)")
            sessionOrder = []
        }
    }

    func saveSessionOrder() {
        do {
            let orderData = SessionOrderData(version: 1, order: sessionOrder)
            let data = try JSONEncoder().encode(orderData)
            try data.write(to: orderFilePath)
            print("💾 Saved session order with \(sessionOrder.count) sessions")
        } catch {
            print("💾 Failed to save session order: \(error)")
            lastError = "Failed to save session order: \(error.localizedDescription)"
        }
    }

    func sortSessionsByOrder(_ sessions: [SessionState]) -> [SessionState] {
        // Key by rowID (compound for relay sessions): two daemons sharing a
        // session_id would otherwise trap `uniqueKeysWithValues` on the dup
        // key (#537). rowID == id for local sessions, so order is unchanged.
        let sessionMap = Dictionary(uniqueKeysWithValues: sessions.map { ($0.rowID, $0) })

        var orderedSessions: [SessionState] = []

        // First, add sessions in the saved order
        for sessionId in sessionOrder {
            if let session = sessionMap[sessionId] {
                orderedSessions.append(session)
            }
        }

        // Then add any new sessions that aren't in the saved order yet
        let orderedIds = Set(sessionOrder)
        let newSessions = sessions.filter { !orderedIds.contains($0.rowID) }

        // Sort new sessions: active first, then by recency (as fallback for new sessions)
        let sortedNewSessions = newSessions.sorted { lhs, rhs in
            if lhs.state == .ready && rhs.state != .ready {
                return false
            } else if lhs.state != .ready && rhs.state == .ready {
                return true
            } else {
                return lhs.updatedAt > rhs.updatedAt
            }
        }

        orderedSessions.append(contentsOf: sortedNewSessions)

        if irrlichtVerboseLogging {
            print("🔢 Sorted \(orderedSessions.count) sessions (\(sessionOrder.count) from saved order, \(sortedNewSessions.count) new)")
        }
        return orderedSessions
    }

    func updateSessionOrder(with sessions: [SessionState]) {
        let currentSessionIds = Set(sessions.map { $0.rowID })
        let orderedSessionIds = sessions.map { $0.rowID }

        // Only update if the order has changed
        if sessionOrder != orderedSessionIds {
            sessionOrder = orderedSessionIds
            saveSessionOrder()
            print("🔄 Updated session order: removed \(Set(sessionOrder).subtracting(currentSessionIds).count), added \(currentSessionIds.subtracting(Set(sessionOrder)).count)")
        }
    }

    func reorderSession(from sourceIndex: Int, to destinationIndex: Int) {
        guard sourceIndex != destinationIndex,
              sourceIndex >= 0, sourceIndex < sessions.count,
              destinationIndex >= 0, destinationIndex <= sessions.count else {
            return
        }

        // Move session in the sessions array
        let session = sessions.remove(at: sourceIndex)
        let adjustedDestination = destinationIndex > sourceIndex ? destinationIndex - 1 : destinationIndex
        sessions.insert(session, at: adjustedDestination)

        // Update the order array to match
        sessionOrder = sessions.map { $0.rowID }
        saveSessionOrder()

        print("🔄 Reordered session \(session.shortId) from \(sourceIndex) to \(adjustedDestination)")
    }

    // MARK: - Duplicate Session Handling

    func assignDuplicateIndexes(_ sessions: inout [SessionState]) {
        // Group sessions by project/branch combination
        var duplicateGroups: [String: [Int]] = [:]

        for (index, session) in sessions.enumerated() {
            let project = session.projectName ?? "unknown"
            let branch = session.gitBranch ?? "no-git"
            let key = "\(project)/\(branch)"

            if duplicateGroups[key] == nil {
                duplicateGroups[key] = []
            }
            duplicateGroups[key]?.append(index)
        }

        // Assign duplicate indexes for groups with more than one session
        for (_, indices) in duplicateGroups {
            if indices.count > 1 {
                for (duplicateNumber, sessionIndex) in indices.enumerated() {
                    sessions[sessionIndex].duplicateIndex = duplicateNumber + 1
                }
            }
        }
    }
}

/// Persisted shape of the session-order file.
private struct SessionOrderData: Codable {
    let version: Int
    let order: [String]
}
