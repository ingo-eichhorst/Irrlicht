import XCTest
import SwiftUI
@testable import Irrlicht

/// Issue #596 — every state glyph must occupy an identical `size × size`
/// layout box. The session row is a plain leading HStack, so an icon whose
/// layout box deviates by even a point shifts the agent number, title, and
/// context bar of that row against its neighbours.
@MainActor
final class SessionStateIconLayoutTests: XCTestCase {
    func testAllStatesShareTheSameLayoutBox() {
        // 12 is the size every production call site uses; 16 guards against
        // regressions that only show up at other sizes (font-metric scaling
        // is not linear in the glyph's point size).
        for size: CGFloat in [12, 16] {
            for state in SessionState.State.allCases {
                let hosting = NSHostingView(rootView: SessionStateIcon(state: state, size: size))
                let fitting = hosting.fittingSize
                XCTAssertEqual(
                    fitting.width, size, accuracy: 0.001,
                    "\(state.rawValue) icon layout width must be exactly \(size)"
                )
                XCTAssertEqual(
                    fitting.height, size, accuracy: 0.001,
                    "\(state.rawValue) icon layout height must be exactly \(size)"
                )
            }
        }
    }
}
