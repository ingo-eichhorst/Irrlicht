import SwiftUI

@main
struct IrrlichtApp: App {
    @StateObject private var sessionManager = SessionManager()
    
    var body: some Scene {
        MenuBarExtra("Irrlicht", systemImage: "lightbulb") {
            SessionListView()
                .environmentObject(sessionManager)
        }
        .menuBarExtraStyle(.window)
    }
}