import XCTest
@testable import Irrlicht

// Pure-logic coverage for the History tab Phase 2 (#750) additions: query
// building, the drilldown axis chain, the metric presets, token formatting, and
// envelope decoding. No snapshots — deterministic and host-free.
final class HistoryPhase2Tests: XCTestCase {
    private func params(_ items: [URLQueryItem]) -> [String: String] {
        Dictionary(uniqueKeysWithValues: items.compactMap { i in i.value.map { (i.name, $0) } })
    }

    func testQueryItemsCarriesChartGroupScope() {
        let p = params(HistoryRange.day.queryItems(
            chart: .tokens, group: .branch,
            scope: HistoryScope(field: .project, value: "irrlicht"),
            forecast: true, customStart: nil, customEnd: nil))
        XCTAssertEqual(p["chart"], "tokens")
        XCTAssertEqual(p["group"], "branch")
        XCTAssertEqual(p["scope"], "project:irrlicht")
        XCTAssertEqual(p["range"], "day")
        XCTAssertEqual(p["forecast"], "true")
    }

    func testQueryItemsCustomRangeSendsStartEndNotRange() {
        let p = params(HistoryRange.custom.queryItems(
            chart: .cost, group: .project, scope: nil,
            forecast: false, customStart: 900, customEnd: 2000))
        XCTAssertEqual(p["start"], "900")
        XCTAssertEqual(p["end"], "2000")
        XCTAssertNil(p["range"])
        XCTAssertNil(p["scope"])
    }

    func testDrillNextAxisChain() {
        XCTAssertEqual(HistoryGroup.project.drillNext, .branch)
        XCTAssertEqual(HistoryGroup.branch.drillNext, .session)
        XCTAssertEqual(HistoryGroup.provider.drillNext, .model)
        XCTAssertEqual(HistoryGroup.model.drillNext, .session)
        XCTAssertNil(HistoryGroup.session.drillNext) // leaf
    }

    func testChartPinnedGroupAndIsCost() {
        XCTAssertEqual(HistoryChart.models.pinnedGroup, .model)
        XCTAssertEqual(HistoryChart.providers.pinnedGroup, .provider)
        XCTAssertNil(HistoryChart.cost.pinnedGroup)
        XCTAssertNil(HistoryChart.tokens.pinnedGroup)
        XCTAssertNil(HistoryChart.co2.pinnedGroup)
        XCTAssertTrue(HistoryChart.cost.isCost)
        XCTAssertTrue(HistoryChart.models.isCost)
        XCTAssertFalse(HistoryChart.tokens.isCost)
        XCTAssertFalse(HistoryChart.co2.isCost)
    }

    // issue #829: the co2 chart is neither the USD nor the tokens metric.
    func testChartIsCO2() {
        XCTAssertTrue(HistoryChart.co2.isCO2)
        XCTAssertFalse(HistoryChart.cost.isCO2)
        XCTAssertFalse(HistoryChart.tokens.isCO2)
        XCTAssertEqual(HistoryChart.co2.label, "CO2")
    }

    func testFormatTokensAndValue() {
        XCTAssertEqual(HistoryFormat.tokens(2_000_000), "2.0M")
        XCTAssertEqual(HistoryFormat.tokens(1500), "1.5k")
        XCTAssertEqual(HistoryFormat.tokens(970), "970")
        XCTAssertEqual(HistoryFormat.value(1.5, chart: .cost), "$1.50")
        XCTAssertEqual(HistoryFormat.value(1500, chart: .tokens), "1.5k")
    }

    // issue #829: unit-adaptive CO2e formatting, matching the web histCO2.
    func testFormatCo2AndValue() {
        XCTAssertEqual(HistoryFormat.co2(0.03), "30mg")
        XCTAssertEqual(HistoryFormat.co2(158.7), "158.7g")
        XCTAssertEqual(HistoryFormat.co2(2850), "2.85kg")
        XCTAssertEqual(HistoryFormat.value(158.7, chart: .co2), "158.7g")
    }

    func testScopeQueryForm() {
        XCTAssertEqual(HistoryScope(field: .branch, value: "main").query, "branch:main")
    }

    func testTokenTypeGroupIsLeafWithLabel() {
        XCTAssertNil(HistoryGroup.tokenType.drillNext) // bands aren't drillable
        XCTAssertEqual(HistoryGroup.tokenType.rawValue, "token_type")
        XCTAssertEqual(HistoryGroup.tokenType.shortLabel, "Type")
        XCTAssertEqual(HistoryTokenType.cacheRead.rawValue, "cache_read")
        XCTAssertEqual(HistoryTokenType.cacheCreation.label, "Cache create")
    }

    func testResponseDecodesTokenSplitAndScope() throws {
        let json = Data("""
        {"range":"day","chart":"tokens","group":"branch","start":0,"end":10,"bucket_seconds":1,"bucket_starts":[0],"total":170,"series":[],"top_contributors":[],"token_split":{"input":100,"output":20,"cache":50},"scope":"project:irrlicht"}
        """.utf8)
        let r = try JSONDecoder().decode(HistoryResponse.self, from: json)
        XCTAssertEqual(r.tokenSplit?.input, 100)
        XCTAssertEqual(r.tokenSplit?.cache, 50)
        XCTAssertEqual(r.scope, "project:irrlicht")
    }

    func testResponsePreV2PayloadDecodesWithNils() throws {
        // A cost×project response with no token_split / scope keys still decodes.
        let json = Data("""
        {"range":"day","chart":"cost","group":"project","start":0,"end":10,"bucket_seconds":1,"bucket_starts":[0],"total":1.5,"series":[],"top_contributors":[]}
        """.utf8)
        let r = try JSONDecoder().decode(HistoryResponse.self, from: json)
        XCTAssertNil(r.tokenSplit)
        XCTAssertNil(r.scope)
    }
}
