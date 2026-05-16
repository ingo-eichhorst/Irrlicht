import XCTest
import SwiftUI
@testable import Irrlicht

/// Table-driven coverage for SessionListView.barColor — the pace-aware
/// ramp that drives quota bar fill color. Keeps the threshold
/// boundaries pinned (issue #309): a change here that flips one of
/// these cases should be deliberate, not accidental.
@MainActor
final class QuotaChipBarColorTests: XCTestCase {

    private static let green = IrrColors.pressureLow

    func testBarColorPaceAwareThresholds() {
        let cases: [(name: String, used: Double, pace: Double?, want: Color)] = [
            ("on pace",                  30, 30,   Self.green),
            ("4pt ahead — still green",  34, 30,   Self.green),
            ("5pt ahead — yellow edge",  35, 30,   .yellow),
            ("14pt ahead — still yellow",44, 30,   .yellow),
            ("15pt ahead — orange edge", 45, 30,   .orange),
            ("far ahead",                70, 30,   .orange),
            ("behind pace",              20, 40,   Self.green),

            // Absolute cap dominates regardless of pace
            ("at cap (85%)",             85, 50,   .orange),
            ("over cap",                 99, 95,   .orange),
            ("84% — boundary just below",84, 50,   .orange), // 34pt over pace, orange via pace rule

            // Pace nil → absolute ramp
            ("nil pace, low usage",      30, nil,  Self.green),
            ("nil pace, 50% — yellow",   50, nil,  .yellow),
            ("nil pace, 70% — orange",   70, nil,  .orange),
            ("nil pace, 90% — orange",   90, nil,  .orange),
        ]
        for c in cases {
            let got = SessionListView.barColor(used: c.used, pace: c.pace)
            XCTAssertEqual(got, c.want,
                "\(c.name): barColor(used: \(c.used), pace: \(String(describing: c.pace))) = \(got), want \(c.want)")
        }
    }
}
