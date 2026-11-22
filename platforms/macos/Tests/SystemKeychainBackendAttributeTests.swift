import XCTest
import Security
@testable import Irrlicht

/// LOCKS over the system backend's attribute dictionaries — pure values that
/// touch no keychain. They pass by construction; what they pin is that moving
/// `write` from delete-then-add to update-in-place kept the item's
/// identity and accessibility exactly as they shipped, and that an update
/// cannot carry anything but the data. Whether `SecItemUpdate` keeps the
/// item's ACL is NOT exercised here (no test touches the real keychain); it
/// rests on reading the API, as `SystemKeychainBackend`'s doc comment says.
final class SystemKeychainBackendAttributeTests: XCTestCase {

    func testTheItemQueryNamesTheShippedItem() {
        let query = SystemKeychainBackend.itemQuery(account: "relayToken")
        XCTAssertEqual(Set(query.keys),
                       [kSecClass as String, kSecAttrService as String, kSecAttrAccount as String])
        XCTAssertEqual(query[kSecClass as String] as? String, kSecClassGenericPassword as String)
        XCTAssertEqual(query[kSecAttrService as String] as? String, "ai.irrlicht.relay")
        XCTAssertEqual(query[kSecAttrAccount as String] as? String, "relayToken")
    }

    func testAnUpdateChangesOnlyTheData() {
        let data = Data("tok".utf8)
        let attrs = SystemKeychainBackend.updateAttributes(data: data)
        XCTAssertEqual(Set(attrs.keys), [kSecValueData as String],
                       "an update must not touch the item's ACL, accessibility or identity")
        XCTAssertEqual(attrs[kSecValueData as String] as? Data, data)
    }

    func testANewItemHasTheShippedAttributes() {
        let data = Data("tok".utf8)
        let attrs = SystemKeychainBackend.addAttributes(account: "relayToken", data: data)
        XCTAssertEqual(Set(attrs.keys),
                       [kSecClass as String, kSecAttrService as String, kSecAttrAccount as String,
                        kSecValueData as String, kSecAttrAccessible as String])
        XCTAssertEqual(attrs[kSecAttrService as String] as? String, "ai.irrlicht.relay")
        XCTAssertEqual(attrs[kSecAttrAccount as String] as? String, "relayToken")
        XCTAssertEqual(attrs[kSecAttrAccessible as String] as? String,
                       kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String)
        XCTAssertEqual(attrs[kSecValueData as String] as? Data, data)
    }
}
