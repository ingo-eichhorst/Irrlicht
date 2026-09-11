import AppKit
import Foundation
import SnapshotTesting
import SwiftUI
import XCTest
@testable import Irrlicht

final class ElfdansPairingClientTests: XCTestCase {
    private let png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

    func testEndpointAcceptsRelaySchemesAndExistingStreamPath() {
        let cases = [
            ("wss://relay.example.com", "https://relay.example.com/api/v1/push/pairings"),
            ("https://relay.example.com/", "https://relay.example.com/api/v1/push/pairings"),
            ("relay.example.com:7839", "http://relay.example.com:7839/api/v1/push/pairings"),
            ("wss://relay.example.com/api/v1/sessions/stream", "https://relay.example.com/api/v1/push/pairings"),
        ]
        for (raw, want) in cases {
            XCTAssertEqual(ElfdansPairingClient.endpointURL(raw)?.absoluteString, want, raw)
        }
    }

    func testEndpointRejectsCredentialsQueryAndUnsupportedScheme() {
        XCTAssertNil(ElfdansPairingClient.endpointURL("https://token@relay.example.com"))
        XCTAssertNil(ElfdansPairingClient.endpointURL("https://relay.example.com?token=secret"))
        XCTAssertNil(ElfdansPairingClient.endpointURL("ftp://relay.example.com"))
    }

    func testMintRequestSendsBearerOnlyToConfiguredRelay() throws {
        let token = "client-secret"
        let request = try XCTUnwrap(ElfdansPairingClient.makeRequest(
            relayURL: "wss://relay.example.com", token: token))
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.absoluteString, "https://relay.example.com/api/v1/push/pairings")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer \(token)")
        XCTAssertNil(request.httpBody)
        XCTAssertNil(ElfdansPairingClient.makeRequest(relayURL: "wss://relay.example.com", token: ""))
    }

    func testDecodesPairingURLAndPNGWithoutAddingTheToken() throws {
        let json = """
        {
          "code":"ABCD-EFGH",
          "expires_in":600,
          "pairing_url":"https://public.example.com/pair/ABCD-EFGH/",
          "pairing_qr":"\(png)"
        }
        """
        let pairing = try ElfdansPairingClient.decode(Data(json.utf8), status: 201)
        XCTAssertEqual(pairing.code, "ABCD-EFGH")
        XCTAssertEqual(pairing.pairingURL, "https://public.example.com/pair/ABCD-EFGH/")
        XCTAssertNotNil(pairing.qrImage)
        XCTAssertFalse(pairing.pairingURL?.contains("client-secret") ?? true)
    }

    func testManualFallbackKeepsCodeAndReason() throws {
        let json = """
        {"code":"ABCD-EFGH","expires_in":600,"pairing_url_reason":"Set --public-url."}
        """
        let pairing = try ElfdansPairingClient.decode(Data(json.utf8), status: 201)
        XCTAssertEqual(pairing.code, "ABCD-EFGH")
        XCTAssertNil(pairing.pairingURL)
        XCTAssertNil(pairing.qrImage)
        XCTAssertEqual(pairing.pairingURLReason, "Set --public-url.")
    }

    func testRejectsNonCreatedAndOversizedResponses() {
        XCTAssertThrowsError(try ElfdansPairingClient.decode(Data("{}".utf8), status: 401))
        XCTAssertThrowsError(try ElfdansPairingClient.decode(Data(repeating: 0, count: 2_000_001), status: 201))
        XCTAssertEqual(
            ElfdansPairingClient.Failure.rejected(401).errorDescription,
            "The relay could not create a pairing code (HTTP 401)."
        )
    }

    func testPairingRequestGateRejectsAnOlderResultAfterSettingsChange() {
        var gate = ElfdansPairingRequestGate()
        let oldRequest = gate.begin()

        gate.invalidate()

        XCTAssertFalse(gate.isCurrent(oldRequest))
        let currentRequest = gate.begin()
        XCTAssertTrue(gate.isCurrent(currentRequest))
    }
}

@MainActor
final class ElfdansPairingViewSnapshotTests: XCTestCase {
    func testReadyPairingRendersAtAReadableSize() throws {
        let pairing = try ElfdansPairingClient.decode(Data("""
        {
          "code":"ABCD-EFGH",
          "expires_in":600,
          "pairing_url":"https://public.example.com/pair/ABCD-EFGH/",
          "pairing_qr":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAQAAAAEAAQMAAABmvDolAAAABlBMVEX///8AAABVwtN+AAABlElEQVR42uyYPe6sMAzEHVGk5Ag5CkdLjpajcARKCsQ8jQ1ZdqF++sfCRSS8v2adib/ktdde+7M2grbKVPZh5cfeXK6AWUSG8yuhhPXb5QWYwAjUjC1ilkxAXT4BEYmLTKW5nALYRFLNWH0CKuEROB7vr9CdAJaQxprDFpdUqeqnJNY5cHJVeMOpSlifC0/fACWsfzrzTChWcfa+AGHFlAgUxSa9SZSPaP0AAVj01J8iZuZb8QUkZh2LhiUfzbdhcwaIZI0DtKCwC6LrqmoPgNWQiJplAOa7qn0AfK/7WTc102L5CZQHYJynApWwpmIWFLo+qnYCAKCqxdoDYI/LTdUeAE1II0priajqS0/rA+B1q48YL5oKLz+q9gFsbPYU4Dh20O4A3u2EMw4alOuSpAOgdeb58IWj5fMFtI0WtG7KOTXfF3d9A21qto4gawNfnsbqvoHLqkdf5T3TOgJsxEw12/k9kPoBbD/JImqVc/MGHOurHKB7ZhuugzegbbT2AYuNnfKQxDoHXnvttf9u/wIAAP//aBj4E7AGYJIAAAAASUVORK5CYII="
        }
        """.utf8), status: 201)
        let host = PinnedSnapshotHost(
            ElfdansPairingDetails(pairing: pairing)
                .padding()
                .background(Color(NSColor.windowBackgroundColor)),
            width: 348, height: 310)
        assertSnapshot(of: host, as: .pinnedImage, named: "ready")
    }
}
