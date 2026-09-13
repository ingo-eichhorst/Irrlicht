import SwiftUI
import XCTest
@testable import Irrlicht

/// The one defect #1955 shipped into its own new UI and then caught in review:
/// the slot-budget stepper rendered with its number invisible.
///
/// **This is the only test in #1955 that was seen RED before its fix existed.**
/// Everything else the change adds is new behaviour with no "before" to run
/// against, and is mutation-proved instead. This one is a genuine defect test
/// in AGENTS.md's sense — the broken construction was measured, the fix was
/// measured, and the two differ by a number you can read in a harness.
///
/// **What went wrong.** The value was written as the `Stepper`'s LABEL and the
/// call site carried `.labelsHidden()` — the modifier whose entire job is to
/// remove a label. The row would have shipped as `Max projects (i) [− +]` with
/// nothing saying what it was set to. `SettingsViewTests` renders the panel but
/// samples only corner-pixel opacity and there is no Settings snapshot suite,
/// so nothing could have failed.
///
/// **Why there is no pixel here.** A snapshot over a digit is the wrong
/// instrument — it goes brittle across hosts and it fails for reasons that have
/// nothing to do with the defect. But "no snapshot" is an argument against
/// snapshots, not against a test: the defect produces a *width*, and the same
/// `PinnedSnapshotHost` measurement that confirmed the fix is the assertion.
///
/// **And there is no magic constant either.** The load-bearing check is
/// relational — a two-digit value must render WIDER than a one-digit one — so
/// it needs no reference number and cannot drift with a font, a scale factor or
/// a host. A hidden number reports the same width for both, which is exactly
/// the failure. The absolute figures are printed, not asserted.
@MainActor
final class MenuBarSlotBudgetStepperTests: XCTestCase {

    /// Host a stepper-shaped view and report the width it wants.
    ///
    /// `PinnedSnapshotHost` pins the appearance, locale, timezone and store, so
    /// two measurements taken through it differ only by what the views differ
    /// by. The frame is generous: a cramped host would compress both arms to
    /// the same number and make the comparison below vacuous, which
    /// `testTheMeasurementCanTellTheTwoConstructionsApart` is the guard for.
    private func hostedWidth(_ view: some View) -> CGFloat {
        PinnedSnapshotHost(view, width: 400, height: 60).view.fittingSize.width
    }

    /// The shipped construction: value in its own `Text`, empty label on the
    /// `Stepper`.
    private struct RealStepper: View {
        @State var value: Int
        var body: some View { MenuBarSlotBudgetStepper(value: $value) }
    }

    /// **Committed mutation fixture** — the construction that shipped broken,
    /// preserved verbatim so the regression is re-run by every suite run rather
    /// than described in a PR body. The value is the `Stepper`'s label and
    /// `.labelsHidden()` removes it.
    private struct LabelInsideStepperMutant: View {
        @State var value: Int
        var body: some View {
            HStack(spacing: 6) {
                Stepper(
                    value: $value,
                    in: MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum
                ) {
                    Text("\(value)").font(.caption).monospacedDigit()
                }
                .labelsHidden()
                .fixedSize()
            }
        }
    }

    /// The bare stepper with no number at all — the floor both arms are
    /// measured against, so "the number takes up space" is stated against
    /// something rather than against a literal.
    private struct BareStepper: View {
        @State var value = 7
        var body: some View {
            HStack(spacing: 6) {
                Stepper(
                    "",
                    value: $value,
                    in: MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum
                )
                .labelsHidden()
                .fixedSize()
            }
        }
    }

    /// The number is on screen, and it is the NUMBER — not a fixed spacer that
    /// happens to occupy the same room.
    ///
    /// Two clauses, neither carrying a reference width:
    /// 1. a one-digit value must render wider than the bare stepper, so the
    ///    value occupies space at all;
    /// 2. a two-digit value must render wider still, so the space it occupies
    ///    tracks the value — which only a rendered number does.
    ///
    /// Seen red before the fix existed: against `LabelInsideStepperMutant`
    /// both clauses fail, which `testTheBrokenConstructionIsRejected` re-runs.
    func testTheSlotBudgetsNumberIsActuallyRendered() {
        let bare = hostedWidth(BareStepper())
        let oneDigit = hostedWidth(RealStepper(value: 7))
        let twoDigits = hostedWidth(RealStepper(value: 10))

        print(String(format: "=== #1955 slot-budget stepper widths, in points: "
                     + "bare %.1f | value 7 -> %.1f | value 10 -> %.1f ===",
                     bare, oneDigit, twoDigits))

        XCTAssertGreaterThan(
            oneDigit, bare,
            "the slot budget's value must occupy width of its own — a stepper whose "
                + "number is hidden measures the same as one with no number"
        )
        XCTAssertGreaterThan(
            twoDigits, oneDigit,
            "a two-digit slot budget must render wider than a one-digit one — if it "
                + "does not, the digits are not on screen"
        )
    }

    /// Mutation-proved (committed): the construction that shipped broken must
    /// fail the very check above.
    ///
    /// Without this, the check could be passing for a reason unrelated to the
    /// defect and nobody would know — the mutant is what makes it evidence.
    func testTheBrokenConstructionIsRejected() {
        let bare = hostedWidth(BareStepper())
        let oneDigit = hostedWidth(LabelInsideStepperMutant(value: 7))
        let twoDigits = hostedWidth(LabelInsideStepperMutant(value: 10))

        print(String(format: "=== #1955 the mutant (value as a hidden Stepper label): "
                     + "bare %.1f | value 7 -> %.1f | value 10 -> %.1f ===",
                     bare, oneDigit, twoDigits))

        // Both clauses of the real check must fail against it. Asserted as one
        // conjunction rather than two, because either one failing is enough to
        // catch the defect and requiring both to fail is the stronger claim
        // about what the mutant is.
        XCTAssertFalse(
            oneDigit > bare && twoDigits > oneDigit,
            "the broken construction passed the number-is-rendered check — that check "
                + "does not catch the defect it was written for"
        )
        XCTAssertEqual(
            oneDigit, twoDigits, accuracy: 0.01,
            "a hidden label makes a one-digit and a two-digit value measure identically; "
                + "if they now differ, this fixture no longer reproduces the defect"
        )
    }

    /// The vacuity guard for the harness itself.
    ///
    /// Every assertion above is a width comparison, so a host that compressed
    /// or clamped both arms to the same number would make all of them
    /// meaningless while staying green. This states the one thing that must be
    /// true for the comparison to mean anything: the two constructions measure
    /// DIFFERENTLY through this harness.
    ///
    /// AGENTS.md: absence of a finding and inability to look must never produce
    /// the same output.
    func testTheMeasurementCanTellTheTwoConstructionsApart() {
        let real = hostedWidth(RealStepper(value: 7))
        let mutant = hostedWidth(LabelInsideStepperMutant(value: 7))
        XCTAssertNotEqual(
            real, mutant, accuracy: 0.01,
            "the shipped and the broken construction measure the same through this "
                + "harness — the checks above are comparing nothing"
        )
        XCTAssertGreaterThan(real, 0, "the harness reported a zero-width render")
    }

    /// The row still honours the type's own bounds after the extraction — the
    /// stepper cannot be driven outside 1…10 from the UI.
    ///
    /// Pinned by source rather than by driving AppKit's `-`/`+` buttons: the
    /// bound is an argument to `Stepper(_:value:in:)`, and what matters is that
    /// it is `MenuBarMaxProjects`' own range rather than a pair of literals
    /// that could drift from the clamp.
    func testTheStepperIsBoundedByTheTypesOwnRange() throws {
        let source = try String(
            contentsOf: URL(fileURLWithPath: #filePath)
                .deletingLastPathComponent()
                .deletingLastPathComponent()
                .appendingPathComponent("Irrlicht/Views/SettingsView.swift"),
            encoding: .utf8
        )
        XCTAssertTrue(
            source.contains("in: MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum"),
            "the stepper must be bounded by MenuBarMaxProjects' own range, not by literals"
        )
        XCTAssertEqual(MenuBarMaxProjects.minimum, 1)
        XCTAssertEqual(MenuBarMaxProjects.maximum, 10)
    }
}
