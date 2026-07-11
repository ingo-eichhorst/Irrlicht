import AppKit
import SwiftUI

/// A segmented control whose segments always split the available width
/// evenly, edge to edge (issue #940).
///
/// SwiftUI's `Picker(pickerStyle: .segmented)` only centers within extra
/// space `.frame(maxWidth: .infinity)` offers — it never stretches into it,
/// because the underlying `NSSegmentedControl` sizes to fit its content and
/// ignores externally imposed width. Bridging to `NSSegmentedControl`
/// directly and setting `segmentDistribution = .fillEqually` is the native
/// fix, mirroring `IrrlichtSwitchToggleStyle`'s precedent of dropping to
/// AppKit when SwiftUI's stock control can't be styled the way the app
/// needs.
struct EqualWidthSegmentedControl: NSViewRepresentable {
    let labels: [String]
    let values: [String]
    @Binding var selection: String
    var tint: Color?

    func makeNSView(context: Context) -> NSSegmentedControl {
        let control = NSSegmentedControl(
            labels: labels,
            trackingMode: .selectOne,
            target: context.coordinator,
            action: #selector(Coordinator.changed(_:))
        )
        control.segmentDistribution = .fillEqually
        return control
    }

    func updateNSView(_ nsView: NSSegmentedControl, context: Context) {
        context.coordinator.parent = self
        if let idx = values.firstIndex(of: selection), nsView.selectedSegment != idx {
            nsView.selectedSegment = idx
        }
        if let tint {
            nsView.selectedSegmentBezelColor = NSColor(tint)
        }
    }

    // Without this, SwiftUI falls back to the NSView's intrinsicContentSize
    // for width — the same "hug content" sizing that makes the native
    // Picker(pickerStyle: .segmented) refuse to stretch. Honoring the
    // proposed width here (paired with `.frame(maxWidth: .infinity)` at the
    // call site) is what actually makes this control fill the row.
    func sizeThatFits(_ proposal: ProposedViewSize, nsView: NSSegmentedControl, context _: Context) -> CGSize? {
        let fitting = nsView.fittingSize
        return CGSize(width: proposal.width ?? fitting.width, height: fitting.height)
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject {
        var parent: EqualWidthSegmentedControl
        init(_ parent: EqualWidthSegmentedControl) { self.parent = parent }

        @objc func changed(_ sender: NSSegmentedControl) {
            guard parent.values.indices.contains(sender.selectedSegment) else { return }
            parent.selection = parent.values[sender.selectedSegment]
        }
    }
}
