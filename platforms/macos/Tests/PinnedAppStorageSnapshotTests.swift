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
        return hostSettingsPanel(store)
    }

    /// The same render with NOTHING seeded — the state #1689's arms need, since
    /// an absent `notificationsEnabled` is the only state
    /// `reconcileNotificationsMasterDefault()` acts on. Split out of
    /// `renderSettingsPanel` rather than parameterised so #1672's helper keeps
    /// meaning exactly what its doc comment says.
    private func hostSettingsPanel(_ store: InMemoryDefaults) -> PinnedSnapshotHost {
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

    // MARK: - A render DECIDES from the store it writes into (#1689)

    /// #1689: `reconcileNotificationsMasterDefault()` read
    /// `UserDefaults.standard.object(forKey:)` to decide whether the master key
    /// was absent, and wrote the answer through `@AppStorage`. Under a pinned
    /// render those are two different stores — the guard consulted the machine
    /// while the write landed here — and `NotificationSettings.masterEnabled()`
    /// defaulted to `.standard` too, so the VALUE came off the machine as well.
    ///
    /// ## What the expected value is, and why it is not written down
    ///
    /// Each arm computes it by calling the app's own
    /// `NotificationSettings.masterEnabled(defaults:)` over a SECOND store
    /// arranged identically — the untouched reference implementation, in the
    /// condition the render leaves the driven store in (a `SessionManager` over
    /// each, so both carry the `register(defaults:)` seed). A hand-written
    /// `true`/`false` per row would be a second copy of the rule that could
    /// drift from it, and the whole obligation here is that the seam did not
    /// change what the reconcile decides — AGENTS.md's #1664 warning, where
    /// `TimeZone.autoupdatingCurrent` read like the obvious mirror and was
    /// measurably wrong.
    ///
    /// ## Why the arms are derived from `allCases`
    ///
    /// The fix reads "any event enabled" from this view's own three
    /// `@AppStorage` toggles — the expression `SettingsView` already uses for
    /// its blocked-notifications hint — instead of
    /// `allCases.contains { defaults.bool(forKey:) }`. Those are two spellings
    /// of one fact, so a fourth `NotificationEvent` that someone forgets to OR
    /// in gets an arm here by existing rather than by being remembered.
    ///
    /// ## The two guards
    ///
    /// `readKeys` is the weak one and is here for fail-loud only: the master
    /// key is read by the toggle's own `@AppStorage` whether the reconcile
    /// consults this store or not, so it proves the panel rendered and asked,
    /// not which store decided. `writtenKeys` is the discriminating one — the
    /// pre-fix guard consulted a machine domain that HAS `notificationsEnabled`
    /// (measured: `com.apple.dt.xctest.tool` holds `= 1`), returned early, and
    /// persisted nothing at all.
    func testTheMasterReconcileDecidesFromThePinnedStoreAndNotTheMachine() {
        var arrangements: [(name: String, enabled: [NotificationEvent])] = [
            ("no event enabled", [])
        ]
        for event in NotificationEvent.allCases {
            arrangements.append(("only \(event.rawValue) enabled", [event]))
        }
        arrangements.append(("every event enabled", NotificationEvent.allCases))

        for arrangement in arrangements {
            let expected = NotificationSettings.masterEnabled(
                defaults: arrangedStore(enabling: arrangement.enabled))

            let store = arrangedStore(enabling: arrangement.enabled)
            let host = hostSettingsPanel(store)
            XCTAssertFalse(host.view.subviews.isEmpty,
                           "\(arrangement.name): the panel hosted nothing — "
                           + "this check cannot have run")
            XCTAssertTrue(
                store.readKeys.contains(NotificationSettings.masterEnabledKey),
                "\(arrangement.name): nothing asked the pinned store for "
                + "\(NotificationSettings.masterEnabledKey), so this arm proves nothing — "
                + "the notifications section did not render")
            XCTAssertTrue(
                store.writtenKeys.contains(NotificationSettings.masterEnabledKey),
                """
                \(arrangement.name): the reconcile persisted no master default into the \
                pinned store. Its guard is deciding from another store — \
                UserDefaults.standard, which under `swift test` is the developer's \
                com.apple.dt.xctest.tool and already holds \
                \(NotificationSettings.masterEnabledKey) = \
                \(String(describing: UserDefaults.standard.object(forKey: NotificationSettings.masterEnabledKey))), \
                so it returns early. Everything this render wrote: \
                \(store.writtenKeys.sorted()).
                """
            )
            XCTAssertEqual(
                store.object(forKey: NotificationSettings.masterEnabledKey) as? Bool, expected,
                """
                \(arrangement.name): the reconcile wrote \
                \(String(describing: store.object(forKey: NotificationSettings.masterEnabledKey))) \
                where the app's own NotificationSettings.masterEnabled(defaults:) computes \
                \(expected) over the same store. The decision is reading preferences this \
                render was not given.
                """
            )
        }
    }

    /// A store carrying nothing but the named events' enable keys, plus the
    /// `register(defaults:)` seed a `SessionManager` puts there — so the
    /// reference implementation and the driven render read stores in the same
    /// condition.
    private func arrangedStore(enabling events: [NotificationEvent]) -> InMemoryDefaults {
        let defaults = InMemoryDefaults()
        for event in events { defaults.set(true, forKey: event.enabledKey) }
        _ = SessionManager(defaults: defaults)
        return defaults
    }

    /// The #940 semantic the reconcile exists inside, as a LOCK: a master key
    /// that is present and `false` is the user's answer, and an enabled event
    /// does not overrule it.
    ///
    /// It discriminates on a FRESH domain, where the pre-fix guard finds no
    /// `notificationsEnabled` in `UserDefaults.standard`, gets past itself, and
    /// writes the machine's per-event OR — `true` on this machine, whose three
    /// `notifyOn*` keys are all `1` — over the `false` this store holds. On THIS
    /// machine it passes both before and after the fix, and one mutation says why
    /// that is not an oversight: **deleting the guard leaves this arm green**,
    /// because the fix passes `master:` into `NotificationSettings.masterEnabled`
    /// and `false ?? anyEventEnabled` is still `false`. The value is held by the
    /// rule, so what the guard actually buys is the arm below, and the two are
    /// separate tests for exactly that reason.
    func testAnExplicitlyDisabledMasterSurvivesARenderWithEveryEventEnabled() {
        let store = arrangedStore(enabling: NotificationEvent.allCases)
        store.set(false, forKey: NotificationSettings.masterEnabledKey)

        let host = hostSettingsPanel(store)
        XCTAssertFalse(host.view.subviews.isEmpty,
                       "the panel hosted nothing — this check cannot have run")
        XCTAssertEqual(
            store.object(forKey: NotificationSettings.masterEnabledKey) as? Bool, false,
            "rendering Settings overrode an explicit `notificationsEnabled = false` — "
            + "the one-time #940 migration is meant to run only while the key is ABSENT")
    }

    /// What the guard buys that the rule does not: a render whose master key is
    /// already there PERSISTS NOTHING. #940 says the migration "runs only while
    /// the key is still absent", and without the guard every render writes the
    /// key back — #1672's write-back loop in a second place, idempotent and
    /// therefore invisible to any value comparison (see
    /// `InMemoryDefaults.writtenKeys`).
    ///
    /// The key is seeded through `register(defaults:)` rather than `set`, which
    /// is the measurement device: registration makes it PRESENT for the read
    /// (`object(forKey:)` merges the two domains, so the optional `@AppStorage`
    /// answers non-nil exactly as the pre-fix `object(forKey:)` did) while
    /// leaving the application domain — the only thing `writtenKeys` reports —
    /// empty, so "the render persisted it" and "the test put it there" cannot be
    /// confused. `true` is seeded rather than `false` so the whole notifications
    /// section renders instead of staying collapsed.
    ///
    /// Note this arrangement is deliberately not one the app produces:
    /// `NotificationSettings.masterEnabledKey` is kept out of `SessionManager`'s
    /// `register(defaults:)` seed on purpose, because a registered value would
    /// disable the upgrade fallback forever.
    func testARenderWhoseMasterKeyIsAlreadyPresentPersistsNothing() {
        let store = arrangedStore(enabling: NotificationEvent.allCases)
        store.register(defaults: [NotificationSettings.masterEnabledKey: true])

        let host = hostSettingsPanel(store)
        XCTAssertFalse(host.view.subviews.isEmpty,
                       "the panel hosted nothing — this check cannot have run")
        XCTAssertTrue(
            store.readKeys.contains(NotificationSettings.masterEnabledKey),
            "nothing asked the pinned store for \(NotificationSettings.masterEnabledKey), "
            + "so 'it persisted nothing' proves nothing")
        XCTAssertFalse(
            store.writtenKeys.contains(NotificationSettings.masterEnabledKey),
            """
            Rendering Settings re-persisted \(NotificationSettings.masterEnabledKey) \
            although the key was already present. The one-time #940 migration is \
            meant to run only while it is ABSENT; writing it on every render is \
            #1672's write-back loop again, and an idempotent write is invisible to \
            every value comparison and to the plist's mtime. Everything this \
            render wrote: \(store.writtenKeys.sorted()).
            """
        )
    }

    // MARK: - The seam reads what the removed code read (#1689)

    /// Reads one key through both `@AppStorage` spellings the fix relies on.
    private struct BoolStorageProbe: View {
        let key: String
        let report: BoolStorageReport

        @AppStorage private var flag: Bool
        @AppStorage private var optional: Bool?

        init(key: String, report: BoolStorageReport) {
            self.key = key
            self.report = report
            self._flag = AppStorage(wrappedValue: false, key)
            self._optional = AppStorage(key)
        }

        var body: some View {
            report.flag = flag
            report.optionalIsNil = optional == nil
            report.rendered = true
            return Color.clear
        }
    }

    private final class BoolStorageReport {
        var flag = false
        var optionalIsNil = true
        var rendered = false
    }

    /// The equivalence the fix rests on, measured rather than argued.
    ///
    /// #1689 replaces two reads with two `@AppStorage` reads of the same keys:
    /// `UserDefaults.standard.object(forKey:) == nil` becomes an optional
    /// `@AppStorage` answering `nil`, and
    /// `allCases.contains { UserDefaults.standard.bool(forKey:) }` becomes the OR
    /// of three `Bool` `@AppStorage`s. Requirement: in the app — where
    /// `@AppStorage` resolves `.standard` — the new reads answer what the old
    /// ones answered. AGENTS.md's #1664 note is why this is a test and not a
    /// sentence: `TimeZone.autoupdatingCurrent` read like the obvious mirror of
    /// `Locale.autoupdatingCurrent` and was measurably wrong.
    ///
    /// The shapes go beyond what the app can produce on purpose. Only the first
    /// three are reachable — these keys are written exclusively through
    /// `@AppStorage(…) var …: Bool` in `SettingsView` (the toggles and this
    /// reconcile) and seeded as a Swift `Bool` by `SessionManager`'s
    /// `register(defaults:)`; `git grep` over the app target finds no other
    /// writer, and the web dashboard's same-named keys live in `localStorage`,
    /// not in this domain. The other five are what a hand-run
    /// `defaults write … -string` can leave there, and they are driven because
    /// they are where the two spellings COULD differ:
    ///
    /// - the optional `@AppStorage` could plausibly have been
    ///   `object(forKey:) as? Bool`, which answers `nil` for a `String` where
    ///   the removed guard's bare `object(forKey:) == nil` answers `false`;
    /// - the `Bool` `@AppStorage` could plausibly not apply
    ///   `bool(forKey:)`'s string/number coercion.
    ///
    /// Measured on macOS 26 / Swift 6.2, neither is the case: all eight shapes
    /// agree on both spellings, so the equivalence is complete rather than
    /// merely covering the reachable set. Every row is therefore asserted, and
    /// the table is printed (`IRR1689 …`) so the measurement is produced by the
    /// run instead of living in this comment.
    func testBothAppStorageSpellingsAnswerWhatTheRemovedReadsAnswered() {
        for shape in Self.coercionShapes {
            let store = InMemoryDefaults()
            if let value = shape.value { store.set(value, forKey: "irrProbeKey") }

            // What the removed code would have answered over this same store.
            let objectWasNil = store.object(forKey: "irrProbeKey") == nil
            let boolRead = store.bool(forKey: "irrProbeKey")

            let report = BoolStorageReport()
            _ = PinnedSnapshotHost(BoolStorageProbe(key: "irrProbeKey", report: report),
                                   width: 40, height: 40, defaults: store)
            XCTAssertTrue(report.rendered,
                          "\(shape.name): the probe never rendered — this check cannot have run")

            print("IRR1689 shape=\(shape.name) objectWasNil=\(objectWasNil) "
                  + "optionalIsNil=\(report.optionalIsNil) "
                  + "bool(forKey:)=\(boolRead) appStorageBool=\(report.flag)")

            XCTAssertEqual(
                report.optionalIsNil, objectWasNil,
                "\(shape.name): the optional @AppStorage disagrees with "
                + "object(forKey:) == nil, which is the guard #1689 replaced")
            XCTAssertEqual(
                report.flag, boolRead,
                "\(shape.name): the Bool @AppStorage disagrees with bool(forKey:), "
                + "which is what NotificationSettings.masterEnabled reads the "
                + "per-event keys through")
        }
    }

    /// Every shape the probe is driven with — the three the app can store at
    /// these keys and five it cannot. See the arm above.
    private static let coercionShapes: [(name: String, value: Any?)] = [
        ("absent", nil),
        ("Bool true", true),
        ("Bool false", false),
        ("Int 1", 1),
        ("Int 0", 0),
        ("String \"1\"", "1"),
        ("String \"YES\"", "YES"),
        ("String \"nonsense\"", "nonsense")
    ]
}
