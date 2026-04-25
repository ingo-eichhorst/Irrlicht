import XCTest

final class ViewTooltipsLintTests: XCTestCase {

    // Regression guard for #218: SwiftUI's `.help(...)` modifier silently stops
    // rendering inside the app's NSPanel, so the codebase uses a custom
    // `.tooltip(...)` AppKit bridge (defined in SessionListView.swift). Any new
    // `.help(...)` call would silently break tooltips again — the kind of
    // regression that snapshot tests can't catch because tooltips don't render
    // until hover. This test fails the build on any non-comment `.help(`
    // occurrence under Irrlicht/.
    func testNoHelpModifierInSwiftUIViews() throws {
        let appURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()        // Tests/
            .deletingLastPathComponent()        // platforms/macos/
            .appendingPathComponent("Irrlicht")

        let fm = FileManager.default
        guard let enumerator = fm.enumerator(at: appURL, includingPropertiesForKeys: nil) else {
            XCTFail("Could not enumerate \(appURL.path)")
            return
        }

        var offenders: [String] = []
        for case let url as URL in enumerator where url.pathExtension == "swift" {
            let source = try String(contentsOf: url, encoding: .utf8)
            let lines = source.split(separator: "\n", omittingEmptySubsequences: false)
            for (idx, rawLine) in lines.enumerated() {
                let line = String(rawLine)
                // Strip line/doc comments — any "//" outside a string literal kills
                // the rest. The codebase has no `.help(` inside string literals, so
                // this naive prefix scan is sufficient.
                let codePart: Substring
                if let commentRange = line.range(of: "//") {
                    codePart = line[..<commentRange.lowerBound]
                } else {
                    codePart = line[...]
                }
                if codePart.contains(".help(") {
                    let rel = url.path.replacingOccurrences(of: appURL.path + "/", with: "")
                    offenders.append("\(rel):\(idx + 1): \(line.trimmingCharacters(in: .whitespaces))")
                }
            }
        }

        XCTAssertTrue(
            offenders.isEmpty,
            """
            Found `.help(...)` modifier — use `.tooltip(...)` instead. \
            `.help()` does not render inside the app's NSPanel (regression of #218).

            \(offenders.joined(separator: "\n"))
            """
        )
    }
}
