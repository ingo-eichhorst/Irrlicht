import XCTest
@testable import Irrlicht

/// Issue #2057: Muse Code's account-API snapshots arrive stamped with
/// provider "meta". Before this ticket the Swift provider registries knew
/// only "anthropic" and "openai", so a Meta chip fell back to the Muse
/// adapter icon and its forecast-strip heading read "Subscription". These
/// assertions were run red against that state before the "meta" entries
/// were added.
@MainActor
final class ProviderIconRegistryTests: XCTestCase {
    func testMetaProviderHasABundledIcon() {
        XCTAssertNotNil(ProviderIconRegistry.image(forKey: "meta"))
    }

    // Lock: the two existing marks still render and an unknown key still
    // returns nil so callers fall back to the adapter icon.
    func testExistingKeysAndUnknownFallback() {
        XCTAssertNotNil(ProviderIconRegistry.image(forKey: "anthropic"))
        XCTAssertNotNil(ProviderIconRegistry.image(forKey: "openai"))
        XCTAssertNil(ProviderIconRegistry.image(forKey: "nonexistent-provider"))
    }

    func testMetaForecastHeadingNamesTheProvider() {
        let vm = QuotaProviderVM(id: "meta", iconKey: "meta", planLabel: nil, windows: [])
        XCTAssertEqual(vm.displayName, "Meta")
    }

    func testMetaQuotaPickerLabelNamesTheAgent() {
        XCTAssertEqual(SettingsView.quotaProviderLabel("meta"), "Muse")
    }
}
