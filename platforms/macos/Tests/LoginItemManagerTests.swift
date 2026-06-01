import XCTest
@testable import Irrlicht
import ServiceManagement

/// Guards the launch-time reconcile decision — the logic that fixes #562,
/// where a failed first registration was never retried. `SMAppService` itself
/// can't run headless, so we only exercise the pure decision table.
final class LoginItemManagerTests: XCTestCase {
    func testPrefersOnRegistersWhenNotEnabled() {
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: true, status: .notRegistered), .register)
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: true, status: .notFound), .register)
        // Re-asserting a pending item is harmless and documents intent.
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: true, status: .requiresApproval), .register)
    }

    func testPrefersOnNoopWhenAlreadyEnabled() {
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: true, status: .enabled), .none)
    }

    func testPrefersOffUnregistersOnlyWhenEnabled() {
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: false, status: .enabled), .unregister)
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: false, status: .notRegistered), .none)
        XCTAssertEqual(LoginItemManager.reconcileAction(prefersOn: false, status: .requiresApproval), .none)
    }
}
