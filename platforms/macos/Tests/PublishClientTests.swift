import XCTest
@testable import Irrlicht
import Foundation

final class PublishClientTests: XCTestCase {

    func testMakeRequestEncodesConfigAsPutJSON() throws {
        let req = try XCTUnwrap(PublishClient.makeRequest(
            enabled: true, url: "ws://localhost:7839", token: "tok"))  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint

        XCTAssertEqual(req.httpMethod, "PUT")
        XCTAssertEqual(req.url?.path, PublishClient.path)
        XCTAssertEqual(req.value(forHTTPHeaderField: "Content-Type"), "application/json")

        let body = try XCTUnwrap(req.httpBody)
        let decoded = try JSONDecoder().decode(PublishClient.Config.self, from: body)
        XCTAssertEqual(decoded, PublishClient.Config(enabled: true, url: "ws://localhost:7839", token: "tok"))  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
    }

    func testMakeRequestCarriesDisableAndEmptyToken() throws {
        // Turning publish off must still send a well-formed body the daemon can
        // act on (enabled=false), not omit fields.
        let req = try XCTUnwrap(PublishClient.makeRequest(enabled: false, url: "", token: ""))
        let body = try XCTUnwrap(req.httpBody)
        let decoded = try JSONDecoder().decode(PublishClient.Config.self, from: body)
        XCTAssertEqual(decoded, PublishClient.Config(enabled: false, url: "", token: ""))
    }
}
