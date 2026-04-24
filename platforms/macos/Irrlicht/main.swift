import AppKit

MainActor.assumeIsolated {
    let delegate = AppDelegate()
    NSApplication.shared.delegate = delegate
    NSApplication.shared.setActivationPolicy(.accessory)
    // `run()` blocks until termination, so `delegate` stays retained
    // for the lifetime of the app (NSApp holds the delegate weakly).
    NSApplication.shared.run()
}
