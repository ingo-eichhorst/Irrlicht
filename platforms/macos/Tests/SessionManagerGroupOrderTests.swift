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
    /// Mutation-checked, run rather than assumed: restoring
    /// `recomposeApiGroups` to the pre-fix `orderedGroups(localApiGroups) +
    /// relayGroups()…` fails this with `["alpha", "beta", "gamma", "delta"]`
    /// against `["alpha", "beta", "gamma"]` — and takes the two relay tests
    /// above down with it.
    func testTheRenderedListAndTheReorderableOrderAreTheSameSequence() {
        let (sut, _) = makeSUT()
        seedLocalPlusRelay(sut)

        XCTAssertEqual(sut.apiGroups.map(\.name), sut.projectGroupOrder)

        sut.moveProjectGroupUp(name: "delta")
        XCTAssertEqual(sut.apiGroups.map(\.name), sut.projectGroupOrder,
                       "a move must leave the two lists in step")
    }

    /// The contract the chevrons rest on, stated as an equivalence rather than
    /// as hard-coded rows: for every rendered top-level group, "the view
    /// offers this move" and "the handler performs it" must be the same
    /// boolean. #1948 broke it in both directions at once — the last local
    /// group was offered a move it could not perform, and the relay group was
    /// offered two.
    ///
    /// Mutation-checked, run rather than assumed: giving
    /// `moveProjectGroupDown` its own bound instead of
    /// `reorderMoves(atIndex:)` fails this with "the down-chevron offer for
    /// gamma disagrees with the handler".
    ///
    /// What it deliberately does NOT catch any more: answering `reorderMoves`
    /// from `apiGroups` instead of `projectGroupOrder`. That was the #1948
    /// defect, and it was measured green under this test once the two lists
    /// became the same sequence — reading either is now equivalent. The
    /// invariant test above is what holds that in place; this one guards the
    /// rule, that one guards the domain.
    func testChevronOffersAgreeWithWhatTheHandlersDo() {
        let (sut, _) = makeSUT()
        seedLocalPlusRelay(sut)

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

    // MARK: - Stale persisted order

    /// A duplicate in the persisted array must not survive into memory, where
    /// it would inflate the count `reorderMoves` reports and give the sort in
    /// `orderedGroups` two entries to choose between.
    ///
    /// The STORE is not rewritten at load — construction stays write-free, and
    /// `orderedGroups` finds nothing to save because the deduped array already
    /// matches the groups. The stored duplicate is harmless: every load drops
    /// it again, and the next real save replaces it, which the move below
    /// shows.
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
}
