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
    /// One connection source and whether it is stuck.
    ///
    /// A value type rather than a pair of loose booleans per source, because
    /// `isFaulted` is the whole rule and it belongs next to the fields it
    /// reads — spelled inline at each call site it becomes the "complex
    /// conditional" this type exists to retire, and it is exactly the kind of
    /// predicate that drifts between two copies (which is how the connection
    /// dot and this banner came to disagree in the first place).
    struct Source {
        /// Whether the user has this source turned on AND it has somewhere to
        /// connect to. A relay toggled on with no URL is not configured.
        let isConfigured: Bool
        /// Whether its reconnect loop has stopped being a transient blip —
        /// set only after three consecutive failures force a `URLSession`
        /// recycle (local #843, relay #846).
        let isStalled: Bool

        var isFaulted: Bool { isConfigured && isStalled }
    }

    /// The faults the app can see on its own, right now.
    ///
    /// A stalled reconnect is not a transient blip: `localConnectionStalled`'s
    /// own doc says it exists "so the UI can show something stronger than
    /// 'reconnecting'". Until #1802 nothing showed anything stronger; it was a
    /// six-point red dot and a tooltip. It matters more than it looks — while
    /// a source is stalled the sessions it feeds are stale, so a row sitting
    /// green is asserting something the app has no current evidence for.
    ///
    /// THE `aggregate != .connected` MASK IS PART OF THE GATE, not a detail of
    /// the connection dot. With both sources configured, one can be carrying
    /// the session list perfectly well while the other is stuck, and
    /// `aggregateConnectionState` already reports that (`.connected` wins).
    /// `SessionListView.statusColor` reads this function's result rather than
    /// re-deriving the rule, so a red banner can no longer appear beside a
    /// green dot.
    static func faults(aggregate: ConnectionState, local: Source, relay: Source) -> [DaemonFault] {
        guard aggregate != .connected else { return [] }
        var out: [DaemonFault] = []
        if local.isFaulted {
            out.append(DaemonFault(
                id: "daemon/unreachable",
                title: "The Irrlicht daemon is not responding",
                reason: "Reconnect attempts keep failing, so the sessions below "
                    + "may be out of date. Irrlicht keeps retrying; if it does "
                    + "not recover, restart the daemon."
            ))
        }
        if relay.isFaulted {
            out.append(DaemonFault(
                id: "relay/unreachable",
                title: "The relay server is not responding",
                reason: "Reconnect attempts keep failing, so sessions from other "
                    + "machines may be missing or out of date. Local sessions are "
                    + "unaffected."
            ))
        }
        return out
    }
}
