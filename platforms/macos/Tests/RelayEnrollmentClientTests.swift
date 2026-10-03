import Foundation
import XCTest
@testable import Irrlicht

final class RelayEnrollmentClientTests: XCTestCase {

    // MARK: - parse

    func testParseAcceptsEnrollmentURLAndYieldsOriginAndCode() throws {
        let cases = [
            "https://relay.example.com/enroll/K7QM-3PXA",
            "  https://relay.example.com/enroll/K7QM-3PXA/\n",
            "https://relay.example.com/enroll/k7qm-3pxa",
            "HTTPS://Relay.Example.com/enroll/K7QM-3PXA",
        ]
        for raw in cases {
            let parsed = try XCTUnwrap(RelayEnrollmentClient.parse(raw), raw)
            XCTAssertEqual(parsed.origin.absoluteString, "https://relay.example.com", raw)
            XCTAssertEqual(parsed.code, "K7QM-3PXA", raw)
        }
    }

    func testParseKeepsAnExplicitPort() throws {
        let parsed = try XCTUnwrap(RelayEnrollmentClient.parse("https://relay.example.com:8443/enroll/K7QM-3PXA"))
        XCTAssertEqual(parsed.origin.absoluteString, "https://relay.example.com:8443")
    }

    func testParseRefusesAnythingButAnHTTPSEnrollmentURL() {
        let refused = [
            "",
            "K7QM-3PXA",                                            // bare code: no origin to send it to
            "http://relay.example.com/enroll/K7QM-3PXA",            // not https
            "wss://relay.example.com/enroll/K7QM-3PXA",
            "https://user:pw@relay.example.com/enroll/K7QM-3PXA",   // credentials
            "https://relay.example.com/enroll/K7QM-3PXA?x=1",       // query
            "https://relay.example.com/enroll/K7QM-3PXA#frag",      // fragment
            "https://relay.example.com/x/enroll/K7QM-3PXA",         // path prefix
            "https://relay.example.com/enroll/K7QM-3PXA/extra",
            "https://relay.example.com/pair/K7QM-3PXA",             // a phone pairing URL
            "https://relay.example.com/enroll/K7QM-3PXI",           // I is not in the alphabet
            "https://relay.example.com/enroll/K7QM-3PX0",           // 0 is not in the alphabet
            "https://relay.example.com/enroll/K7QM3PXA",            // missing dash
            "https://relay.example.com/enroll/K7QM-3PXAB",          // wrong length
            "https:///enroll/K7QM-3PXA",                            // no host
        ]
        for raw in refused {
            XCTAssertNil(RelayEnrollmentClient.parse(raw), raw)
        }
    }

    // The relay prints only the bare code when it has no --public-url, and
    // tells the operator it "still works when typed or pasted into the
    // desktop app by hand" (core/cmd/irrlichtrelay/pairing_handoff.go).
    func testParseAcceptsABareCodeAgainstAnAlreadyConfiguredSecureRelay() throws {
        for relay in ["wss://relay.example.com", "https://relay.example.com/",
                      "wss://relay.example.com/api/v1/sessions/stream"] {
            let parsed = try XCTUnwrap(RelayEnrollmentClient.parse(" k7qm-3pxa ", configuredRelayURL: relay), relay)
            XCTAssertEqual(parsed.origin.absoluteString, "https://relay.example.com", relay)
            XCTAssertEqual(parsed.code, "K7QM-3PXA", relay)
        }
    }

    func testParseRefusesABareCodeWithoutASecureConfiguredRelay() {
        for relay in ["", "ws://localhost:7839", "http://relay.example.com", "localhost:7839"] {
            XCTAssertNil(RelayEnrollmentClient.parse("K7QM-3PXA", configuredRelayURL: relay), relay)
        }
        XCTAssertNil(RelayEnrollmentClient.parse("K7QM-3PXI", configuredRelayURL: "wss://relay.example.com"))
    }

    func testAFullURLWinsOverTheConfiguredRelay() throws {
        let parsed = try XCTUnwrap(RelayEnrollmentClient.parse(
            "https://other.example.com/enroll/K7QM-3PXA", configuredRelayURL: "wss://relay.example.com"))
        XCTAssertEqual(parsed.origin.absoluteString, "https://other.example.com")
    }

    // MARK: - request

    func testRedeemRequestPostsCodeAndLabelWithoutAnyBearer() throws {
        let parsed = try XCTUnwrap(RelayEnrollmentClient.parse("https://relay.example.com/enroll/K7QM-3PXA"))
        let request = RelayEnrollmentClient.makeRequest(origin: parsed.origin, code: parsed.code, label: "Studio Mac")
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.absoluteString, "https://relay.example.com/api/v1/enroll/redeem")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))
        let body = try XCTUnwrap(request.httpBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: String])
        XCTAssertEqual(json, ["code": "K7QM-3PXA", "label": "Studio Mac"])
    }

    // MARK: - decode

    func testDecodeReturnsTheIssuedToken() throws {
        let data = Data(#"{"token":"irr_secret","token_id":"tok_1"}"#.utf8)
        XCTAssertEqual(try RelayEnrollmentClient.decode(data, status: 200), "irr_secret")
    }

    func testDecodeRefusesASuccessWithoutAToken() {
        XCTAssertThrowsError(try RelayEnrollmentClient.decode(Data(#"{"token":""}"#.utf8), status: 200)) {
            XCTAssertEqual($0 as? RelayEnrollmentClient.Failure, .invalidResponse)
        }
    }

    func testEachRelayRefusalReadsDifferently() {
        let relayError = { (msg: String) in Data(#"{"error":"\#(msg)"}"#.utf8) }
        let cases: [(Int, Data, RelayEnrollmentClient.Failure)] = [
            (401, relayError("invalid or expired enrollment code"), .codeRefused),
            (403, relayError("enrollment requires --auth tokens-file"), .authOff),
            (429, relayError("too many failed enrollment attempts, retry later"), .rateLimited),
            (500, relayError("issuing device token failed"), .rejected(500, "issuing device token failed")),
            (502, Data("<html>bad gateway</html>".utf8), .rejected(502, nil)),
        ]
        var descriptions: [String] = []
        for (status, data, want) in cases {
            XCTAssertThrowsError(try RelayEnrollmentClient.decode(data, status: status), "\(status)") {
                XCTAssertEqual($0 as? RelayEnrollmentClient.Failure, want, "\(status)")
                descriptions.append($0.localizedDescription)
            }
        }
        XCTAssertEqual(Set(descriptions).count, cases.count, "every refusal needs its own message: \(descriptions)")
    }

    func testRefusedCodeSaysMintAFreshOneAndAuthOffNamesTheFlag() {
        let refused = RelayEnrollmentClient.Failure.codeRefused.errorDescription ?? ""
        let authOff = RelayEnrollmentClient.Failure.authOff.errorDescription ?? ""
        XCTAssertTrue(refused.contains("enroll new"), refused)
        XCTAssertTrue(authOff.contains("--auth off"), authOff)
        XCTAssertNotEqual(refused, authOff)
    }

    func testUnreachableOriginReadsAsANetworkProblemNotABadCode() {
        let failure = RelayEnrollmentClient.Failure.unreachable("relay.example.com")
        let text = failure.errorDescription ?? ""
        XCTAssertTrue(text.contains("relay.example.com"), text)
        XCTAssertNotEqual(text, RelayEnrollmentClient.Failure.codeRefused.errorDescription)
    }

    // MARK: - apply

    func testApplyWritesTokenURLAndPublishFlag() throws {
        let defaults = InMemoryDefaults()
        defaults.set(false, forKey: "publishToRelay")
        var written: [(value: String, account: String)] = []
        let parsed = try XCTUnwrap(RelayEnrollmentClient.parse("https://relay.example.com/enroll/K7QM-3PXA"))

        try RelayEnrollmentClient.apply(token: "irr_secret", origin: parsed.origin, defaults: defaults) { value, account in
            written.append((value, account))
            return true
        }

        XCTAssertEqual(written.map(\.value), ["irr_secret"])
        XCTAssertEqual(written.map(\.account), ["relayToken"])
        XCTAssertEqual(defaults.string(forKey: "relayServerURL"), "https://relay.example.com")
        XCTAssertTrue(defaults.bool(forKey: "publishToRelay"))
    }

    func testApplyLeavesSettingsAloneWhenTheKeychainRefusesTheToken() throws {
        let defaults = InMemoryDefaults()
        defaults.set(false, forKey: "publishToRelay")
        defaults.set("wss://old.example.com", forKey: "relayServerURL")
        let parsed = try XCTUnwrap(RelayEnrollmentClient.parse("https://relay.example.com/enroll/K7QM-3PXA"))

        XCTAssertThrowsError(try RelayEnrollmentClient.apply(
            token: "irr_secret", origin: parsed.origin, defaults: defaults) { _, _ in false }) {
            XCTAssertEqual($0 as? RelayEnrollmentClient.Failure, .keychainWriteFailed)
        }
        XCTAssertEqual(defaults.string(forKey: "relayServerURL"), "wss://old.example.com")
        XCTAssertFalse(defaults.bool(forKey: "publishToRelay"))
    }
}
