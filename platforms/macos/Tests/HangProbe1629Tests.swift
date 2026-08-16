import XCTest

/// TEMPORARY PROBE FOR #1629 — REMOVED BEFORE THIS PR MERGES.
///
/// Exists for exactly one reason: to make `macos-swift.yml`'s `swift-test`
/// step run a suite that genuinely fails on a runner, so that whether
/// `swift_suite_verdict` gets to speak is *observable* instead of reasoned
/// about. A local `bash` reproduction of the `-e` swallow is not evidence
/// about a GitHub runner.
///
/// A hang rather than an `XCTFail` on purpose: the issue's complaint is that a
/// hang and an ordinary assertion failure are indistinguishable in CI, so the
/// diagnosis worth proving reachable is the one only a hang produces —
/// `swift test HUNG: no exit within 240s`, `last test to start: …`, and the
/// TRUNCATED explanation that follows it.
///
/// 600s is deliberately larger than `SWIFT_SUITE_TIMEOUT` (240s) so the
/// harness's own bound is what fires, and deliberately smaller than the job's
/// `timeout-minutes: 20` so that even a tree-kill that failed would end on its
/// own rather than burning the job cap.
final class HangProbe1629Tests: XCTestCase {
    func testDeliberateHangSoTheVerdictHasSomethingToDiagnose() {
        Thread.sleep(forTimeInterval: 600)
    }
}
