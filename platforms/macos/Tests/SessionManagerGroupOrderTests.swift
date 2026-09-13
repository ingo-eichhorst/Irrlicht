import XCTest
@testable import Irrlicht
import Foundation

/// Coverage for the project-group reorder chevrons (#1948).
///
/// `git grep -n "moveProjectGroup" HEAD -- platforms/macos/Tests` returned
/// nothing before this file existed, so the handlers shipped in #93, regressed
/// in #172 and regressed again in #1948 without a single test noticing.
@MainActor
final class SessionManagerGroupOrderTests: XCTestCase {
    typealias AgentGroup = SessionManager.AgentGroup

    private func makeSUT(order: [String] = []) -> (SessionManager, InMemoryDefaults) {
        let defaults = InMemoryDefaults()
        if !order.isEmpty { defaults.set(order, forKey: "projectGroupOrder") }
        return (SessionManager(defaults: defaults), defaults)
    }

    /// Three local groups plus one relay-only group — the shape that made the
    /// last local group's down-chevron render enabled while the handler
    /// refused the move.
    ///
    /// The relay half goes in through `handleRelayMessage`, not by assigning
    /// `relaySessionMap`: that map is keyed by `rowID` (`"daemon/id"`), so a
    /// hand-seeded entry is a shape production never builds and would stay
    /// green if the ingest path changed its key.
    ///
    /// Locals first, relay second — a name enters `projectGroupOrder` when it
    /// is first rendered, so arrival order decides the initial positions. The
    /// reverse sequence is legitimate and puts `delta` first; these tests want
    /// the ordinary one, where the local daemon is already connected.
    private func seedLocalPlusRelay(_ sut: SessionManager) {
        sut.seedLocalApiGroups(["alpha", "beta", "gamma"].map { AgentGroup(name: $0) })
        sut.handleRelayMessage(
            RelayFixtures.push(source: "daemonB", sessionId: "r1", project: "delta")
        )
    }

    // MARK: - Reading the persisted order

    /// The order must be in place the moment construction returns.
    /// `loadProjectGroupOrder()` used to run inside the deferred `Task` in
    /// `SessionManager.init`, leaving a window where a hydration can reach
    /// `orderedGroups` against an empty order — and that function's own
    /// `saveProjectGroupOrder()` call then writes the daemon's order over the
    /// user's. Closing the window, not evidence the window was ever hit.
    func testConstructionReadsThePersistedOrderSynchronously() {
        let (sut, _) = makeSUT(order: ["gamma", "beta", "alpha"])
        XCTAssertEqual(
            sut.projectGroupOrder, ["gamma", "beta", "alpha"],
            "the persisted order was not readable until after init returned"
        )
    }

    // MARK: - Moving a group (locks — no coverage existed before this file)

    /// A plain all-local move, both directions. This passes on the unfixed
    /// code too, so it is a LOCK on behaviour that must not change, not
    /// evidence of the defect.
    func testMovingAGroupReordersAndPersists() {
        let (sut, defaults) = makeSUT()
        sut.seedLocalApiGroups(["alpha", "beta", "gamma"].map { AgentGroup(name: $0) })

        sut.moveProjectGroupDown(name: "alpha")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["beta", "alpha", "gamma"])
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"),
                       ["beta", "alpha", "gamma"])

        sut.moveProjectGroupUp(name: "alpha")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "gamma"])
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"),
                       ["alpha", "beta", "gamma"])
    }

    // MARK: - The two lists the chevrons straddle

    /// Every rendered top-level group is reorderable, relay-only ones
    /// included. Reported against #1948: "moving of items works for local, but
    /// not for remote". The reorderable order therefore spans the whole
    /// rendered list, not just its local half.
    func testARelayOnlyGroupIsRenderedAndIsReorderable() {
        let (sut, _) = makeSUT()
        seedLocalPlusRelay(sut)

        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "gamma", "delta"])
        XCTAssertEqual(sut.projectGroupOrder, ["alpha", "beta", "gamma", "delta"])
        XCTAssertNotNil(sut.reorderMoves(for: "delta"),
                        "a rendered group the user can see must be movable")
    }

    /// The move itself, across the local/relay boundary in both directions —
    /// the behaviour the report asked for.
    func testARelayOnlyGroupMovesAmongTheLocalOnes() {
        let (sut, defaults) = makeSUT()
        seedLocalPlusRelay(sut)

        sut.moveProjectGroupUp(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "delta", "gamma"])

        sut.moveProjectGroupUp(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "delta", "beta", "gamma"])
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"),
                       ["alpha", "delta", "beta", "gamma"])

        sut.moveProjectGroupDown(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "delta", "gamma"])
    }

    /// The invariant that makes the #1948 class unrepresentable rather than
    /// merely guarded: the rendered top-level list and the reorderable order
    /// are the same names in the same sequence. While that holds, "which list
    /// did the view measure against" has no wrong answer.
    ///
    /// RESTATED for #1954, not weakened. The reorderable order is now
    /// `renderedGroupOrder` — the remembered superset narrowed to what is on
    /// screen — because `projectGroupOrder` deliberately keeps names that have
    /// left. Asserting against the raw array would have to be deleted for the
    /// superset to ship, and deleting it is exactly how #1948 comes back: the
    /// superset is what makes an absent name available to pad a bound. So the
    /// invariant is re-stated against the projection and given the case that
    /// only exists now — a remembered name that is NOT rendered — where the
    /// two arrays genuinely differ and only one of them is the domain.
    ///
    /// Phase 1's mutation record is #1949's, run there and not re-run here:
    /// restoring `recomposeApiGroups` to the pre-#1949 `orderedGroups(
    /// localApiGroups) + relayGroups()…` failed it with
    /// `["alpha", "beta", "gamma", "delta"]` against
    /// `["alpha", "beta", "gamma"]`. Phase 2's is #1954's, run through
    /// `tools/mutate.sh`: restoring the `currentNames.contains($0)` prune to
    /// `orderedGroups` fails it at the precondition —
    /// `XCTAssertTrue failed - precondition: the remembered order must still
    /// hold delta, or the two arrays never diverge and this phase asserts
    /// nothing` — which is the phase failing LOUDLY rather than passing
    /// vacuously once the superset is gone.
    func testTheRenderedListAndTheReorderableOrderAreTheSameSequence() {
        let (sut, _) = makeSUT()
        seedLocalPlusRelay(sut)

        // Phase 1 — everything remembered is rendered, so all three agree.
        XCTAssertEqual(sut.apiGroups.map(\.name), sut.renderedGroupOrder)
        XCTAssertEqual(sut.apiGroups.map(\.name), sut.projectGroupOrder)

        sut.moveProjectGroupUp(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), sut.renderedGroupOrder,
                       "a move must leave the two lists in step")

        // Phase 2 — delta leaves. The remembered array keeps it; the rendered
        // list and the reorderable domain must still be the same sequence.
        sut.handleRelayMessage(
            RelayFixtures.delete(source: "daemonB", sessionId: "r1", project: "delta")
        )
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "gamma"],
                       "precondition: delta must really leave the rendered payload")
        XCTAssertTrue(sut.projectGroupOrder.contains("delta"),
                      "precondition: the remembered order must still hold delta, "
                      + "or the two arrays never diverge and this phase asserts nothing")

        XCTAssertEqual(sut.apiGroups.map(\.name), sut.renderedGroupOrder)
        XCTAssertNotEqual(sut.apiGroups.map(\.name), sut.projectGroupOrder,
                          "the raw remembered array is no longer the domain; if it "
                          + "still matched, the projection would be untested")

        sut.moveProjectGroupUp(name: "gamma")
        XCTAssertEqual(sut.apiGroups.map(\.name), sut.renderedGroupOrder,
                       "a move with an absent name remembered must leave them in step")
    }

    /// The contract the chevrons rest on, stated as an equivalence rather than
    /// as hard-coded rows: for every rendered top-level group, "the view
    /// offers this move" and "the handler performs it" must be the same
    /// boolean. #1948 broke it in both directions at once — the last local
    /// group was offered a move it could not perform, and the relay group was
    /// offered two.
    ///
    /// Run twice, and the second arm is the #1954 half: once every remembered
    /// name is rendered, the raw remembered array and the projection are the
    /// same list, so an offer measured against the wrong one is
    /// indistinguishable. With `delta` remembered but gone they differ.
    ///
    /// Mutation-checked, run rather than assumed — `tools/mutate.sh` against
    /// `swift test --filter SessionManagerGroupOrderTests`. Both mutations
    /// leave arm 1 green and take arm 2 down, which is the evidence that arm
    /// 2 is load-bearing:
    ///
    /// - the bound in `reorderMoves(atIndex:in:)` taken from
    ///   `projectGroupOrder` instead of `domain`:
    ///   `Swift/ContiguousArrayBuffer.swift:692: Fatal error: Index out of
    ///   range`, in this test, on the `delta`-absent arm;
    /// - the same substitution in `reorderMoves(for:)` only, so the offer and
    ///   the handler disagree rather than trapping:
    ///   `XCTAssertEqual failed: ("true") is not equal to ("false") - the
    ///   down-chevron offer for gamma disagrees with the handler`.
    ///
    /// What it deliberately does NOT catch: answering `reorderMoves` from
    /// `apiGroups` rather than from the projection of the remembered order.
    /// Those are the same sequence by construction, which is what the
    /// invariant test above holds in place; this one guards the rule, that one
    /// guards the domain.
    func testChevronOffersAgreeWithWhatTheHandlersDo() {
        for absentNameRemembered in [false, true] {
            let (sut, _) = makeSUT()
            seedLocalPlusRelay(sut)
            if absentNameRemembered {
                sut.handleRelayMessage(
                    RelayFixtures.delete(source: "daemonB", sessionId: "r1", project: "delta")
                )
                XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "gamma"],
                               "precondition: delta must really leave the rendered payload")
                XCTAssertTrue(sut.projectGroupOrder.contains("delta"),
                              "precondition: delta must stay remembered, or this arm "
                              + "repeats the first one")
            }

            for name in sut.apiGroups.map(\.name) {
                let saved = sut.projectGroupOrder
                let moves = sut.reorderMoves(for: name)

                sut.moveProjectGroupUp(name: name)
                XCTAssertEqual(moves?.up ?? false, sut.projectGroupOrder != saved,
                               "the up-chevron offer for \(name) disagrees with the handler")
                sut.projectGroupOrder = saved
                sut.recomposeApiGroups()

                sut.moveProjectGroupDown(name: name)
                XCTAssertEqual(moves?.down ?? false, sut.projectGroupOrder != saved,
                               "the down-chevron offer for \(name) disagrees with the handler")
                sut.projectGroupOrder = saved
                sut.recomposeApiGroups()
            }
        }
    }

    // MARK: - Stale persisted order

    /// A duplicate in the persisted array must not survive into memory, where
    /// it would inflate the count `reorderMoves` reports and give the sort in
    /// `orderedGroups` two entries to choose between.
    ///
    /// The STORE is not rewritten at load — construction stays write-free, and
    /// `orderedGroups` finds nothing to save because no incoming name is
    /// first-seen, so its only write never fires. (Before #1954 the reason was
    /// "the deduped array already matches the groups"; the conclusion is the
    /// same, the mechanism is not — post-#1954 nothing would be saved even if
    /// the array did NOT match.) The stored duplicate is harmless: every load
    /// drops it again, and the next real save replaces it, which the move
    /// below shows.
    ///
    /// Mutation-checked, run rather than assumed: dropping the dedupe from
    /// `loadProjectGroupOrder` fails the first assertion with
    /// `["alpha", "alpha", "beta"]`.
    func testADuplicateInThePersistedOrderIsHealedOnLoad() {
        let (sut, defaults) = makeSUT(order: ["alpha", "alpha", "beta"])
        XCTAssertEqual(sut.projectGroupOrder, ["alpha", "beta"])

        sut.seedLocalApiGroups(["alpha", "beta"].map { AgentGroup(name: $0) })
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta"])

        sut.moveProjectGroupDown(name: "alpha")
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"), ["beta", "alpha"])
    }

    /// The other duplicate source, which the load-time dedupe cannot see: two
    /// incoming groups sharing a name.
    ///
    /// Mutation-checked, run rather than assumed: dropping
    /// `seen.insert($0).inserted` from `orderedGroups` fails this with
    /// `["dup", "dup"]`.
    func testTwoIncomingGroupsSharingANameYieldOneOrderEntry() {
        let (sut, _) = makeSUT()

        sut.seedLocalApiGroups([AgentGroup(name: "dup"), AgentGroup(name: "dup")])

        XCTAssertEqual(sut.projectGroupOrder, ["dup"])
    }

    // MARK: - A name that leaves the payload keeps its slot (#1954)

    /// A relay group the user moved, that leaves the payload and comes back,
    /// returns to the slot the user gave it.
    ///
    /// #1954: `orderedGroups` pruned `projectGroupOrder` down to the names in
    /// each payload, so any recompose without `delta` — a `session_deleted`, a
    /// relay disconnect, `restoreDaemon`, a hydration that has not landed yet
    /// — dropped the name, and the next push re-appended it LAST. #1949 is
    /// what made this reach relay rows: before it, relay names were never in
    /// the array at all.
    ///
    /// `delta` leaves through the real ingest path — a `session_deleted`
    /// frame through `handleRelayMessage` — not by editing `relaySessionMap`.
    /// That map is keyed by `rowID`, so a hand-seeded removal is a shape
    /// production never builds and would stay green if the delete arm changed
    /// its key. `RelayFixtures` states the same rule for the create side.
    ///
    /// SEEN RED before the fix existed, at `a680d5ea`:
    /// `XCTAssertEqual failed: ("["alpha", "beta", "gamma", "delta"]") is not
    /// equal to ("["delta", "alpha", "beta", "gamma"]")`, and the persisted
    /// array with it. Re-run red under `tools/mutate.sh` with the
    /// `currentNames.contains($0)` prune restored.
    func testARelayGroupThatLeavesAndReturnsKeepsItsSlot() {
        let (sut, defaults) = makeSUT()
        seedLocalPlusRelay(sut)

        sut.moveProjectGroupUp(name: "delta")
        sut.moveProjectGroupUp(name: "delta")
        sut.moveProjectGroupUp(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), ["delta", "alpha", "beta", "gamma"],
                       "precondition: the user moved delta to the top")

        sut.handleRelayMessage(
            RelayFixtures.delete(source: "daemonB", sessionId: "r1", project: "delta")
        )
        // Not a formality: if the delete frame missed, everything below would
        // pass without the absence it is about ever happening.
        XCTAssertEqual(sut.apiGroups.map(\.name), ["alpha", "beta", "gamma"],
                       "precondition: delta must really leave the rendered payload")

        sut.handleRelayMessage(
            RelayFixtures.push(source: "daemonB", sessionId: "r1", project: "delta")
        )

        XCTAssertEqual(sut.apiGroups.map(\.name), ["delta", "alpha", "beta", "gamma"],
                       "delta came back at the end instead of the slot the user gave it")
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"),
                       ["delta", "alpha", "beta", "gamma"],
                       "the persisted order lost delta's slot while it was absent")
    }

    /// An empty payload writes nothing at all.
    ///
    /// The worst case of the same mechanism, and the one measured in #1954's
    /// log as `💾 Saved project group order with 0 groups`: a recompose runs
    /// before the first successful hydration, `localApiGroups` is still empty,
    /// and the pruning `orderedGroups` saved `[]` over the user's whole order.
    /// A payload without a name is not evidence the user stopped caring where
    /// it sits, and an empty one is not evidence of anything.
    ///
    /// The write COUNT, not just the value: re-saving the identical array
    /// satisfies every value assertion and is still the write this forbids.
    /// `InMemoryDefaults.writeCount(forKey:)` is what separates them.
    ///
    /// SEEN RED before the fix existed, at `a680d5ea`, on all three:
    /// `("2") is not equal to ("1")`, `("Optional([])") is not equal to
    /// ("Optional(["alpha", "beta", "gamma"])")`, and `("[]") is not equal to
    /// ("["alpha", "beta", "gamma"]")`. That run's own log printed the issue's
    /// line, `Saved project group order with 0 groups`. Re-run red under
    /// `tools/mutate.sh` with the `currentNames.contains($0)` prune restored.
    func testAnEmptyPayloadWritesNothing() {
        let (sut, defaults) = makeSUT(order: ["alpha", "beta", "gamma"])
        sut.seedLocalApiGroups(["alpha", "beta", "gamma"].map { AgentGroup(name: $0) })
        let storedBefore = defaults.stringArray(forKey: "projectGroupOrder")
        let writesBefore = defaults.writeCount(forKey: "projectGroupOrder")
        XCTAssertEqual(storedBefore, ["alpha", "beta", "gamma"],
                       "precondition: the store holds the user's order")

        sut.seedLocalApiGroups([])

        // Vacuity guard: without it, a recompose that never ran and a
        // recompose that ran and wrote nothing read identically.
        XCTAssertTrue(sut.apiGroups.isEmpty,
                      "precondition: the empty payload must reach the recompose")
        XCTAssertEqual(defaults.writeCount(forKey: "projectGroupOrder"), writesBefore,
                       "an empty payload wrote to the store")
        XCTAssertEqual(defaults.stringArray(forKey: "projectGroupOrder"), storedBefore,
                       "an empty payload changed the persisted order")
        XCTAssertEqual(sut.projectGroupOrder, ["alpha", "beta", "gamma"],
                       "an empty payload forgot the remembered order in memory")
    }
}
