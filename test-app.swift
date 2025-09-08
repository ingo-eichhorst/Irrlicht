#!/usr/bin/env swift

import Foundation

// Simple test to verify SessionState parsing works
struct SessionState: Codable {
    let id: String
    let state: State
    let model: String
    let cwd: String
    let transcriptPath: String
    let updatedAt: Date
    let eventCount: Int
    let lastEvent: String
    
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd
        case transcriptPath = "transcript_path"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
    }
    
    enum State: String, CaseIterable, Codable {
        case working, waiting, ready
    }
}

// Test reading the instance files
let instancesPath = FileManager.default.homeDirectoryForCurrentUser
    .appendingPathComponent("Library")
    .appendingPathComponent("Application Support")
    .appendingPathComponent("Irrlicht")
    .appendingPathComponent("instances")

print("🔍 Checking instances directory: \(instancesPath.path)")

do {
    let fileURLs = try FileManager.default.contentsOfDirectory(
        at: instancesPath,
        includingPropertiesForKeys: nil,
        options: [.skipsHiddenFiles]
    ).filter { $0.pathExtension == "json" }
    
    print("📄 Found \(fileURLs.count) JSON files:")
    
    for fileURL in fileURLs {
        print("  • \(fileURL.lastPathComponent)")
        
        do {
            let data = try Data(contentsOf: fileURL)
            let session = try JSONDecoder().decode(SessionState.self, from: data)
            
            print("    ✅ Parsed: \(session.id) - \(session.state.rawValue) - \(session.model)")
        } catch {
            print("    ❌ Parse error: \(error)")
        }
    }
} catch {
    print("❌ Directory read error: \(error)")
}