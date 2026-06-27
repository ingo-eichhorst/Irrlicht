import XCTest
@testable import Irrlicht

// Pure-logic coverage for the History tab Phase 2 (#750) additions: query
// building, the drilldown axis chain, the metric presets, token formatting, and
// envelope decoding. No snapshots — deterministic and host-free.
final class HistoryPhase2Tests: XCTestCase {
    private func params(_ items: [URLQueryItem]) -> [String: String] {
        Dictionary(uniqueKeysWithValues: items.compactMap { i in i.value.map { (i.name, $0) } })
    }

    func testQueryItems_carriesChartGroupScope() {
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

    func testQueryItems_customRangeSendsStartEndNotRange() {
        let p = params(HistoryRange.custom.queryItems(
            chart: .cost, group: .project, scope: nil,
            forecast: false, customStart: 900, customEnd: 2000))
        XCTAssertEqual(p["start"], "900")
        XCTAssertEqual(p["end"], "2000")
        XCTAssertNil(p["range"])
        XCTAssertNil(p["scope"])
    }

    func testDrillNext_axisChain() {
        XCTAssertEqual(HistoryGroup.project.drillNext, .branch)
        XCTAssertEqual(HistoryGroup.branch.drillNext, .session)
        XCTAssertEqual(HistoryGroup.provider.drillNext, .model)
        XCTAssertEqual(HistoryGroup.model.drillNext, .session)
        XCTAssertNil(HistoryGroup.session.drillNext) // leaf
    }

    func testChart_pinnedGroupAndIsCost() {
        XCTAssertEqual(HistoryChart.models.pinnedGroup, .model)
        XCTAssertEqual(HistoryChart.providers.pinnedGroup, .provider)
        XCTAssertNil(HistoryChart.cost.pinnedGroup)
        XCTAssertNil(HistoryChart.tokens.pinnedGroup)
        XCTAssertTrue(HistoryChart.cost.isCost)
        XCTAssertTrue(HistoryChart.models.isCost)
        XCTAssertFalse(HistoryChart.tokens.isCost)
    }

    func testFormat_tokensAndValue() {
        XCTAssertEqual(HistoryFormat.tokens(2_000_000), "2.0M")
        XCTAssertEqual(HistoryFormat.tokens(1500), "1.5k")
        XCTAssertEqual(HistoryFormat.tokens(970), "970")
        XCTAssertEqual(HistoryFormat.value(1.5, chart: .cost), "$1.50")
        XCTAssertEqual(HistoryFormat.value(1500, chart: .tokens), "1.5k")
    }

    func testScope_queryForm() {
        XCTAssertEqual(HistoryScope(field: .branch, value: "main").query, "branch:main")
    }

    func testResponse_decodesTokenSplitAndScope() throws {
        let json = Data("""
        {"range":"day","chart":"tokens","group":"branch","start":0,"end":10,"bucket_seconds":1,"bucket_starts":[0],"total":170,"series":[],"top_contributors":[],"token_split":{"input":100,"output":20,"cache":50},"scope":"project:irrlicht"}
        """.utf8)
        let r = try JSONDecoder().decode(HistoryResponse.self, from: json)
        XCTAssertEqual(r.tokenSplit?.input, 100)
        XCTAssertEqual(r.tokenSplit?.cache, 50)
        XCTAssertEqual(r.scope, "project:irrlicht")
    }

    func testResponse_preV2PayloadDecodesWithNils() throws {
        // A cost×project response with no token_split / scope keys still decodes.
        let json = Data("""
        {"range":"day","chart":"cost","group":"project","start":0,"end":10,"bucket_seconds":1,"bucket_starts":[0],"total":1.5,"series":[],"top_contributors":[]}
        """.utf8)
        let r = try JSONDecoder().decode(HistoryResponse.self, from: json)
        XCTAssertNil(r.tokenSplit)
        XCTAssertNil(r.scope)
    }
}
