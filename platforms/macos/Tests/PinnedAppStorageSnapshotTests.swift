import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Locks the property #1662 is about: a rendered view reads the preferences it
/// is GIVEN, not the ones the machine happens to hold — and a suite that
/// supplies them writes nothing it has to put back.
///
/// ## What was wrong
///
/// `GroupViewSnapshotTests` and `SessionRowSnapshotTests` each read eight real
/// `UserDefaults.standard` keys into `Any?` fields in `setUp`, overwrote them,
/// and restored them in `tearDown`. `AdapterIconAppearanceTests` did the same
/// for six. Under `swift test` that domain is `com.apple.dt.xctest.tool` — a
/// persistent plist in the developer's own `~/Library/Preferences` — so the
/// arrangement failed in three separate ways:
///
/// 1. It was **per-suite**, not a property of the snapshot strategy: a new
///    suite hosting one of these views rendered under whatever the domain
///    happened to hold. That is the complaint #1659 made about the time-zone
///    `setUp`, one family later.
/// 2. The pinned set was **smaller than the set the views read**. Measured on
///    this machine, that domain also carried `advancedSettingsExpanded`,
///    `backchannelActivation`, `menuBarStyle`, `notificationsEnabled`,
///    `notifyOnContextPressure`, `notifyOnReady`, `notifyOnWaiting`,
///    `projectGroupOrder` and `taskEtaActivation` — nine keys nothing pinned,
///    one of them holding the developer's real project names.
/// 3. A **`tearDown` is not a guarantee**. #1523 aborts the process mid-run,
///    `tools/lib/swift-suite.sh` kills the tree at 240s and `--budget` kills
///    the gate; none of those run a `tearDown`, so the pinned values stayed in
///    a domain the NEXT run reads.
///
/// ## What replaces it
///
/// `PinnedSnapshotHost` applies `.defaultAppStorage(_:)` over an
/// `InMemoryDefaults`, so every `@AppStorage` in the hosted subtree resolves
/// through a store that touches no domain at all, and `SessionManager` takes
/// the store it reads its own persisted preferences from. There is nothing to
/// restore, so nothing is left behind by a run that never reaches its
/// `tearDown` — point 3 is answered by removing the state rather than by
/// unwinding it more carefully. The parameter is typed `InMemoryDefaults` and
/// not `UserDefaults`, so `.standard` is not expressible at a snapshot host at
/// all — point 1, held by the type the way the locale and time-zone pins are.
///
/// ## The shape of every arm here
///
/// The trap this suite is written around is `PinnedScaleSnapshotTests`':
/// asserting "renders with cost hidden" is green on a machine whose domain
/// already says so, whether the pin reached the view or not. So each arm drives
/// **two** preference sets through one view and requires them to disagree —
/// whatever the machine holds, at most one arm can agree with it — and
/// everything goes through `PinnedSnapshotHost` itself rather than a hosting
/// view assembled here, so "the host that pins" and "the host the proof was
/// taken on" cannot be two objects that disagree.
@MainActor
final class PinnedAppStorageSnapshotTests: XCTestCase {

    // MARK: - The pin reaches the pixels

    private func costedGroup() -> SessionManager.AgentGroup {
        SessionManager.AgentGroup(
            name: "alpha",
            agents: [],
            costs: ["day": 12.34, "week": 56.78, "month": 90.12, "year": 345.67]
        )
    }

    /// Rasterise one `GroupView` through the host the suites use, under a store
    /// the caller has arranged.
    ///
    /// `store: nil` means "pass no argument", i.e. exactly what a real suite
    /// writes when it wants isolation and nothing else.
    private func rasterizedGroup(store: InMemoryDefaults?) -> Data {
        let manager = SessionManager(defaults: store ?? InMemoryDefaults())
        let group = costedGroup()
        manager.apiGroups = [group]
        let content = GroupView(group: group)
            .environmentObject(manager)
            .frame(width: 350, height: 48)
            .background(Color(NSColor.windowBackgroundColor))
        let host = store.map {
            PinnedSnapshotHost(content, width: 350, height: 48, defaults: $0)
        } ?? PinnedSnapshotHost(content, width: 350, height: 48)
        let image = PinnedScaleSnapshot.rasterize(host.view, scale: PinnedScaleSnapshot.referenceScale)
        // "could not rasterise" and "rasterised to the same thing" must never
        // produce the same verdict.
        guard let data = image.tiffRepresentation, !data.isEmpty else {
            XCTFail("the group row rasterised to nothing — this check cannot have run")
            return Data()
        }
        return data
    }

    private func store(_ pairs: [String: Any]) -> InMemoryDefaults {
        let defaults = InMemoryDefaults()
        for (key, value) in pairs { defaults.set(value, forKey: key) }
        return defaults
    }

    /// The load-bearing arm, and the acceptance test #1662 asks for: two
    /// preference sets, one view, and the renders must differ.
    ///
    /// This is what a reverted pin fails. Delete `.defaultAppStorage(defaults)`
    /// from `PinnedSnapshotHost` and both renders come from
    /// `UserDefaults.standard` — identical — which is what this refuses.
    func testTheSameViewRendersDifferentlyUnderTwoPinnedPreferenceSets() {
        let shown = store(["showCostDisplay": true, "projectCostTimeframe": "day"])
        let hidden = store(["showCostDisplay": false, "projectCostTimeframe": "day"])

        // Rendering must be deterministic first, or "they differ" proves
        // nothing about the preferences.
        XCTAssertEqual(rasterizedGroup(store: store(["showCostDisplay": true])),
                       rasterizedGroup(store: store(["showCostDisplay": true])),
                       "the same view rasterised twice under the same store differs — "
                       + "the both-sides arms below are not measuring preferences")

        XCTAssertNotEqual(
            rasterizedGroup(store: shown), rasterizedGroup(store: hidden),
            "the group row rendered identically with showCostDisplay true and false — "
            + "the pinned store is reaching nothing, and the render is coming from "
            + "UserDefaults.standard (showCostDisplay = "
            + "\(String(describing: UserDefaults.standard.object(forKey: "showCostDisplay"))))")
    }

    /// A second key, chosen because it changes rendered TEXT rather than
    /// presence: `$12.34 / day` against `$345.67 / year`. A pin that reached
    /// only booleans would pass the arm above.
    func testTheSameViewRendersDifferentlyUnderTwoCostTimeframes() {
        let day = store(["showCostDisplay": true, "projectCostTimeframe": "day"])
        let year = store(["showCostDisplay": true, "projectCostTimeframe": "year"])
        XCTAssertNotEqual(
            rasterizedGroup(store: day), rasterizedGroup(store: year),
            "the group row rendered identically under projectCostTimeframe day and year")
    }

    /// …and the store a suite gets when it names none carries NOTHING from the
    /// machine, so the arms above cannot pass while every real snapshot renders
    /// under someone's Settings.
    ///
    /// Stated as an emptiness check rather than as "is not `.standard`" because
    /// that is the property that actually matters and it fails loudly if the
    /// parameter is ever re-typed to `UserDefaults` and defaulted to
    /// `.standard`. The second assertion is its vacuity guard: on a hypothetical
    /// machine whose process domain were also empty, the first would be
    /// satisfiable by the very thing it is supposed to exclude.
    func testTheHostsDefaultStoreCarriesNothingFromTheMachine() {
        let host = PinnedSnapshotHost(Color.clear, width: 10, height: 10)
        XCTAssertTrue(
            host.defaults.dictionaryRepresentation().isEmpty,
            "the host's default preference store is not empty — it carries "
            + "\(host.defaults.dictionaryRepresentation().keys.sorted())")
        XCTAssertFalse(
            UserDefaults.standard.dictionaryRepresentation().isEmpty,
            "UserDefaults.standard is empty in this process, so the assertion above "
            + "cannot tell an isolated store from the process domain")
    }

    // MARK: - Every key the deleted setUps pinned, and every key they missed

    /// What a hosted subtree actually read. A class so the SwiftUI value type
    /// can write into it.
    private final class AppStorageReport {
        var values: [String: String] = [:]
        var rendered = false
    }

    /// One `@AppStorage` per key named in #1662 — the eight the two snapshot
    /// suites pinned by hand and the nine they did not — read back as strings.
    ///
    /// Every declaration repeats the app's own default, so a key that failed to
    /// resolve through the pinned store reports that default rather than the
    /// driven value, and the arm below names it.
    private struct AppStorageProbe: View {
        @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
        @AppStorage("projectCostTimeframe") private var projectCostTimeframe: String = CostTimeframe.day.rawValue
        @AppStorage("displayMode") private var displayMode: String = DisplayMode.context.rawValue
        @AppStorage("debugMode") private var debugMode: Bool = false
        @AppStorage("costDisplayMode") private var costDisplayMode: String = "cost"
        @AppStorage(ContextPressureThreshold.valueKey) private var thresholdValue: Double = ContextPressureThreshold.defaultValue
        @AppStorage(ContextPressureThreshold.unitKey) private var thresholdUnit: String = ContextPressureThreshold.defaultUnit.rawValue
        @AppStorage("advancedSettingsExpanded") private var advancedSettingsExpanded: Bool = false
        @AppStorage("backchannelActivation") private var backchannelActivation: Bool = false
        @AppStorage(MenuBarStyle.storageKey) private var menuBarStyle: String = MenuBarStyle.lights.rawValue
        @AppStorage(NotificationSettings.masterEnabledKey) private var notificationsEnabled: Bool = false
        @AppStorage(NotificationEvent.contextPressure.enabledKey) private var notifyOnContextPressure: Bool = false
        @AppStorage(NotificationEvent.ready.enabledKey) private var notifyOnReady: Bool = false
        @AppStorage(NotificationEvent.waiting.enabledKey) private var notifyOnWaiting: Bool = false
        @AppStorage("taskEtaActivation") private var taskEtaActivation: Bool = false

        let report: AppStorageReport

        var body: some View {
            report.values = [
                "showCostDisplay": String(showCostDisplay),
                "projectCostTimeframe": projectCostTimeframe,
                "displayMode": displayMode,
                "debugMode": String(debugMode),
                "costDisplayMode": costDisplayMode,
                ContextPressureThreshold.valueKey: String(thresholdValue),
                ContextPressureThreshold.unitKey: thresholdUnit,
                "advancedSettingsExpanded": String(advancedSettingsExpanded),
                "backchannelActivation": String(backchannelActivation),
                MenuBarStyle.storageKey: menuBarStyle,
                NotificationSettings.masterEnabledKey: String(notificationsEnabled),
                NotificationEvent.contextPressure.enabledKey: String(notifyOnContextPressure),
                NotificationEvent.ready.enabledKey: String(notifyOnReady),
                NotificationEvent.waiting.enabledKey: String(notifyOnWaiting),
                "taskEtaActivation": String(taskEtaActivation)
            ]
            report.rendered = true
            return Color.clear
        }
    }

    /// Values chosen so that every one of them differs from the app's declared
    /// default for its key — asserted below rather than eyeballed, because a
    /// row that accidentally matched its default would be green whether the
    /// store reached the view or not.
    private static let drivenValues: [String: Any] = [
        "showCostDisplay": true,
        "projectCostTimeframe": CostTimeframe.year.rawValue,
        "displayMode": DisplayMode.history60s.rawValue,
        "debugMode": true,
        "costDisplayMode": "co2",
        ContextPressureThreshold.valueKey: 37.0,
        ContextPressureThreshold.unitKey: ContextPressureThreshold.Unit.tokens.rawValue,
        "advancedSettingsExpanded": true,
        "backchannelActivation": true,
        MenuBarStyle.storageKey: MenuBarStyle.combined.rawValue,
        NotificationSettings.masterEnabledKey: true,
        NotificationEvent.contextPressure.enabledKey: true,
        NotificationEvent.ready.enabledKey: true,
        NotificationEvent.waiting.enabledKey: true,
        "taskEtaActivation": true
    ]

    private func probeReport(store: InMemoryDefaults?) -> AppStorageReport {
        let report = AppStorageReport()
        let probe = AppStorageProbe(report: report)
        _ = store.map { PinnedSnapshotHost(probe, width: 40, height: 40, defaults: $0) }
            ?? PinnedSnapshotHost(probe, width: 40, height: 40)
        return report
    }

    /// Every `@AppStorage` key #1662 names resolves through the host's store —
    /// the eight the deleted `setUp`s pinned AND the nine they missed.
    ///
    /// The nine are the point. Nothing about the mechanism distinguishes them
    /// from the eight, which is exactly why a fix that extended the hand-kept
    /// lists would have had to name them and a fix that moves the pin onto the
    /// type does not. This arm names them anyway, so "covered by construction"
    /// is a measurement rather than an argument.
    func testEveryKeyNamedInTheIssueResolvesThroughTheHostsStore() {
        let driven = probeReport(store: store(Self.drivenValues))
        XCTAssertTrue(driven.rendered, "the probe never rendered — this check cannot have run")

        let untouched = probeReport(store: nil)
        XCTAssertTrue(untouched.rendered, "the probe never rendered — this check cannot have run")

        for key in Self.drivenValues.keys.sorted() {
            guard let seen = driven.values[key], let fallback = untouched.values[key] else {
                XCTFail("the probe reported nothing for \(key) — it does not read that key")
                continue
            }
            // The vacuity guard, per key: a driven value equal to the app's own
            // default would be green whether the store reached the view or not.
            XCTAssertNotEqual(
                seen, fallback,
                "\(key) reads \(seen) both when driven and when unset — that row proves nothing")
        }
    }

    // MARK: - The preferences SessionManager owns, not the views

    /// `summaryDisplayMode` and `projectGroupOrder` are not `@AppStorage`;
    /// `SessionManager` reads them itself, which is why the deleted `setUp`s
    /// had to pin `summaryDisplayMode` in the process domain and why
    /// `projectGroupOrder` — the key holding this developer's real project
    /// names — was reachable at all.
    func testSessionManagerReadsItsPersistedPreferencesFromTheStoreItIsGiven() {
        let collapsed = store([
            "summaryDisplayMode": SummaryDisplayMode.collapsed.rawValue,
            "projectGroupOrder": ["gamma", "beta", "alpha"]
        ])
        let waiting = store([
            "summaryDisplayMode": SummaryDisplayMode.waiting.rawValue,
            "projectGroupOrder": ["alpha"]
        ])

        XCTAssertEqual(SessionManager(defaults: collapsed).summaryDisplayMode, .collapsed)
        XCTAssertEqual(SessionManager(defaults: waiting).summaryDisplayMode, .waiting)
    }

    /// Building a manager over an empty store WRITES nothing into it — so the
    /// app does not start persisting a preference the user never set, and a
    /// suite's store stays a statement of what that suite asked for.
    func testBuildingASessionManagerWritesNoPreference() {
        let empty = InMemoryDefaults()
        _ = SessionManager(defaults: empty)
        XCTAssertNil(empty.object(forKey: "summaryDisplayMode"),
                     "constructing a SessionManager persisted summaryDisplayMode")
        XCTAssertNil(empty.object(forKey: "projectGroupOrder"),
                     "constructing a SessionManager persisted projectGroupOrder")
    }

    /// The WRITE direction, and the one that makes the abort path moot: toggling
    /// the mode on an injected manager leaves the process domain byte-for-byte
    /// as it was, so there is nothing for a `tearDown` to put back and nothing
    /// for an aborted run to leave behind.
    func testTogglingTheModeLeavesTheProcessDomainUntouched() {
        let injected = InMemoryDefaults()
        let before = UserDefaults.standard.object(forKey: "summaryDisplayMode") as? String

        let manager = SessionManager(defaults: injected)
        manager.summaryDisplayMode = .collapsed

        XCTAssertEqual(injected.string(forKey: "summaryDisplayMode"),
                       SummaryDisplayMode.collapsed.rawValue,
                       "the write did not reach the injected store")
        XCTAssertEqual(UserDefaults.standard.object(forKey: "summaryDisplayMode") as? String, before,
                       "the write reached UserDefaults.standard — the seam leaks into the "
                       + "developer's com.apple.dt.xctest.tool domain")
    }

    // MARK: - A render must PERSIST nothing (#1672)

    /// The keys #1672 is about.
    private static let soundKeys = Set(NotificationEvent.allCases.map(\.soundKey))

    /// `SettingsView`, hosted the way a snapshot suite hosts a view, over
    /// `store`.
    ///
    /// `notificationsEnabled` is seeded true because the three
    /// `NotificationEventRow`s live behind the master gate (#940) and a
    /// collapsed section renders none of them — which is precisely the shape a
    /// "wrote nothing" assertion passes vacuously under.
    private func renderSettingsPanel(_ store: InMemoryDefaults) -> PinnedSnapshotHost {
        store.set(true, forKey: NotificationSettings.masterEnabledKey)
        let view = SettingsView(isPresented: .constant(true),
                                showPermissionsReview: .constant(false),
                                sessionManager: SessionManager(defaults: store))
            .environmentObject(UpdateManager(startingUpdater: false))
            .environmentObject(DaemonManager())
        return PinnedSnapshotHost(view,
                                  width: SessionListView.panelWidth,
                                  height: 520,
                                  defaults: store)
    }

    /// #1672, as a property of a render: showing the notification rows writes
    /// no sound choice into the store they resolved through.
    ///
    /// ## Why this asserts over `writtenKeys` and not `object(forKey:)`
    ///
    /// `SessionManager.init` puts all three sound keys into the store's
    /// REGISTRATION domain, so `object(forKey:)` answers non-nil for every one
    /// of them whether anything persisted or not — that is why
    /// `testBuildingASessionManagerWritesNoPreference` above can use it (its two
    /// keys are not in that seed) and why this one cannot. It is also why the
    /// defect survived #1662's audit against a real domain: the row wrote back
    /// the value `register(defaults:)` had just handed it, so no value changed,
    /// the plist's mtime never moved, and every merged view of the domain read
    /// identically. Only the application domain's KEY SET tells a persisted
    /// default from a registered one.
    ///
    /// ## The vacuity guard
    ///
    /// A row that never rendered — the master gate is collapsed by default, so
    /// that is the likely accident — and a row whose `@AppStorage` resolved
    /// `UserDefaults.standard` instead of the pinned store both also write
    /// nothing here. `readKeys` separates them: the rows must have ASKED this
    /// store for all three keys.
    ///
    /// ## Why the sound keys and not the whole written set
    ///
    /// Scoped deliberately. Measured on this machine, a clean render writes
    /// exactly `["notificationsEnabled"]` — the key this test seeds itself — so
    /// a full-set equality would pass today. It is not asserted anyway:
    /// `SettingsView`'s `.onAppear` reconciles `taskEtaActivation` and
    /// `backchannelActivation` against the daemon (SettingsView.swift:413-452)
    /// inside a `Task`, and those write `@AppStorage` keys whenever the daemon
    /// answers before the assertion runs. Pinning the whole set would make this
    /// a timing assertion dressed as a preference one, and its failures would
    /// name a race rather than #1672. The general "no app code writes the
    /// process domain directly" claim is `PersistentDefaultsLintTests`' third
    /// rule; this arm is #1672's own. The failure message prints the whole set
    /// regardless, so a surprise there is still legible.
    func testRenderingTheNotificationRowsPersistsNoSoundChoice() {
        let store = InMemoryDefaults()
        let host = renderSettingsPanel(store)
        XCTAssertFalse(host.view.subviews.isEmpty,
                       "the panel hosted nothing — this check cannot have run")

        for key in Self.soundKeys.sorted() {
            XCTAssertTrue(
                store.readKeys.contains(key),
                "the rows never read \(key) from the pinned store, so 'nothing was written' "
                + "proves nothing — either the section stayed collapsed or the row is "
                + "resolving UserDefaults.standard")
        }

        XCTAssertEqual(
            store.writtenKeys.intersection(Self.soundKeys), [],
            """
            Rendering the notification rows PERSISTED a sound choice — a value \
            the user never picked. In the app that lands in their real \
            io.irrlicht.app domain; under `swift test` it lands in the \
            developer's com.apple.dt.xctest.tool, which is where #1672 found \
            soundOnReady = funk and soundOnContextPressure = sosumi. Everything \
            this render wrote: \(store.writtenKeys.sorted()).
            """
        )
    }
}
