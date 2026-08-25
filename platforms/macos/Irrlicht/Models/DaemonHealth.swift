import Foundation

/// One daemon-wide fault (#1802).
///
/// Irrlicht's own internal failures split in two, and only one belongs on a
/// session. A failure scoped to ONE session — this transcript is unreadable,
/// this adapter's store is locked — sets that session's `error` state and
/// renders on its row. A failure with no session to attach to — the daemon is
/// not answering at all, hook installation is broken, no adapters loaded —
/// has nowhere to land, and used to reach the user as nothing, or as a
/// six-point dot with a tooltip. This type is the second half.
///
/// Deliberately NOT `SessionError`. That type is the agent's verdict about one
/// conversation and carries retry counters, an HTTP status and a provider
/// class; a daemon fault has none of those and would be all-nil in every field
/// that matters. Two shapes rather than one over-general one, for the same
/// reason `SessionError` is a separate channel from a failed tool call.
struct DaemonFault: Identifiable, Equatable {
    /// Stable within a render pass, so SwiftUI's `ForEach` does not rebuild
    /// the row on every republish of the same standing fault.
    let id: String
    /// What is broken, in three or four words.
    let title: String
    /// Why, and — where there is one — what to do about it. Rendered verbatim
    /// and in full: this is the only text that distinguishes two faults that
    /// share a title.
    let reason: String
}

/// The daemon-wide banner's content, or `nil` when there is nothing to say.
///
/// The show/hide decision lives HERE and not in the view, mirroring
/// `UnappliedGrantSummary` exactly: a failable initializer that returns `nil`
/// on an empty list is the whole hide mechanism, the parent unwraps an
/// `Optional`, and the view itself takes a non-optional summary and always
/// renders. That split is what lets "healthy produces no banner" be asserted
/// as a plain `XCTAssertNil` — a semantic claim — instead of by photographing
/// the absence of a strip, which is a brittle way to assert nothing.
struct DaemonErrorSummary: Equatable {
    let text: String
    let items: [DaemonFault]

    /// Derived, not stored: a second copy of the length is state that has to
    /// be kept in sync with `items` by hand.
    var count: Int { items.count }

    init?(items: [DaemonFault]) {
        guard !items.isEmpty else { return nil }
        self.items = items
        text = items.count == 1
            ? "Irrlicht has a problem"
            : "Irrlicht has \(items.count) problems"
    }
}

/// Derives the daemon-wide faults the app can observe.
///
/// Pure, static, and free of `SessionManager` so the decision is unit-testable
/// without a live manager — the same shape `MenuBarImageBuilder.iconState` uses
/// for the same reason.
///
/// WHAT IT DOES NOT COVER, said plainly rather than implied: #1802's issue
/// names "hook installation broken" and "no adapters loaded" as the motivating
/// daemon-wide faults, and this build reports NEITHER. Both are daemon-internal
/// conditions, and no daemon payload the macOS app decodes carries them today —
/// verified against every top-level field the app reads: the WebSocket envelope
/// (`SessionManager+WebSocket.swift`), `GET /api/v1/sessions`, `GET
/// /api/v1/permissions`, and `GET /state`. Adding that wire field is #1801's
/// half of this epic (Go wire + web), not this one's, and inventing a second
/// spelling of it here would guarantee the two clients disagree.
///
/// So this is built as the array it will need to be: when the daemon does
/// report its own faults, they append to `faults(...)`'s result and every
/// layer above — summary, banner, tests — is unchanged.
enum DaemonHealth {
    /// The faults the app can see on its own, right now.
    ///
    /// `localConnectionStalled` is the one genuinely daemon-wide fault macOS
    /// can observe with no wire change, and it is not a transient blip: it is
    /// set only after three consecutive failed reconnects have forced a
    /// `URLSession` recycle (#843), whose own doc says it exists "so the UI can
    /// show something stronger than 'reconnecting'". Until now nothing showed
    /// anything stronger — it rendered as a red six-point dot and a tooltip.
    /// This is that stronger thing.
    ///
    /// It matters more than it looks: while the local connection is stalled
    /// every session on screen is stale, so a row sitting green is asserting
    /// something the app has no current evidence for. A user who cannot see
    /// that reads the panel as up to date.
    static func faults(useLocalDaemon: Bool, localConnectionStalled: Bool) -> [DaemonFault] {
        var out: [DaemonFault] = []
        // Gated on `useLocalDaemon` for the same reason the connection dot is
        // (SessionListView's status dot): a relay-only setup has no local
        // daemon to be stalled, and the flag can be left set from an earlier
        // local session.
        if useLocalDaemon && localConnectionStalled {
            out.append(DaemonFault(
                id: "daemon/unreachable",
                title: "The Irrlicht daemon is not responding",
                reason: "Reconnect attempts keep failing, so the sessions below "
                    + "may be out of date. Irrlicht keeps retrying; if it does "
                    + "not recover, restart the daemon."
            ))
        }
        return out
    }
}
