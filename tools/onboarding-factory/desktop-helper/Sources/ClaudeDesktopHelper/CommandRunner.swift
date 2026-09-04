import DesktopHelperCore
import Foundation

enum CommandRunner {
    static func run(_ request: HelperRequest) throws -> HelperResponse {
        let limits = try RequestValidator.validate(request)
        let context = try ClaudeDesktopContext.load()

        switch request.command {
        case .preflight:
            return HelperResponse(ok: true, command: request.command, status: context.status)
        case .inspect:
            try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
            let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
            return HelperResponse(
                ok: true,
                command: request.command,
                status: context.status,
                elements: tree.snapshots
            )
        case .probe:
            let definitions = request.probes!
            try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
            let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
            let controls = try ProbeValidator.validate(
                elements: tree.snapshots,
                definitions: definitions
            )
            return HelperResponse(
                ok: true,
                command: request.command,
                status: context.status,
                controls: controls
            )
        case .setValue:
            return try setValue(request, context: context, limits: limits)
        case .keyboard:
            return try keyboard(request, context: context, limits: limits)
        case .physicalClick:
            return try physicalClick(request, context: context, limits: limits)
        }
    }

    private static func setValue(
        _ request: HelperRequest,
        context: ClaudeDesktopContext,
        limits: TraversalLimits
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        guard let value = request.value else {
            throw HelperFailure(.invalidRequest, "set_value requires value.")
        }
        try context.requireFrontmostForAction()
        try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
        let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
        let target = try tree.unique(matching: selector)
        try AXRuntime.setValue(value, on: target.element)
        let postcondition = Postcondition(
            selector: selector,
            condition: .valueEquals,
            value: value
        )
        try awaitPostcondition(postcondition, context: context, limits: limits)
        return HelperResponse(
            ok: true,
            command: request.command,
            status: context.status,
            action: ActionResult(
                kind: request.command.rawValue,
                targetPath: target.snapshot.path,
                valueRedacted: true,
                postcondition: .valueEquals
            )
        )
    }

    private static func keyboard(
        _ request: HelperRequest,
        context: ClaudeDesktopContext,
        limits: TraversalLimits
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        guard let keyCode = request.keyCode, keyCode <= 127 else {
            throw HelperFailure(.invalidRequest, "keyboard requires keyCode in 0...127.")
        }
        let postcondition = try requiredPostcondition(request.postcondition)
        try context.requireFrontmostForAction()
        try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
        let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
        let target = try tree.unique(matching: selector)
        try AXRuntime.focus(target.element)
        try AXRuntime.postKeyboardEvent(keyCode: keyCode, modifiers: request.modifiers ?? [])
        try awaitPostcondition(postcondition, context: context, limits: limits)
        return HelperResponse(
            ok: true,
            command: request.command,
            status: context.status,
            action: ActionResult(
                kind: request.command.rawValue,
                targetPath: target.snapshot.path,
                valueRedacted: true,
                postcondition: postcondition.condition
            )
        )
    }

    private static func physicalClick(
        _ request: HelperRequest,
        context: ClaudeDesktopContext,
        limits: TraversalLimits
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        let postcondition = try requiredPostcondition(request.postcondition)
        try context.requireFrontmostForAction()
        try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
        let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
        let target = try tree.unique(matching: selector)

        // Re-read the selected AX object immediately before the click. The
        // request cannot supply a point. A stale object or invalid frame fails.
        let fresh = AXRuntime.snapshot(
            target.element,
            path: target.snapshot.path,
            hierarchy: target.snapshot.hierarchy
        )
        guard ControlFinder.matches(fresh, selector: selector), let frame = fresh.frame else {
            throw HelperFailure(.staleControl, "The selected control became stale before the click.")
        }
        let plan = try ClickPlan(freshFrame: frame)
        try AXRuntime.requireHitTarget(target.element, at: plan.point)
        try AXRuntime.physicalClick(plan.point)
        try awaitPostcondition(postcondition, context: context, limits: limits)
        return HelperResponse(
            ok: true,
            command: request.command,
            status: context.status,
            action: ActionResult(
                kind: request.command.rawValue,
                targetPath: target.snapshot.path,
                valueRedacted: true,
                postcondition: postcondition.condition
            )
        )
    }

    private static func requiredSelector(_ selector: ControlSelector?) throws -> ControlSelector {
        guard let selector, !selector.isEmpty else {
            throw HelperFailure(.invalidRequest, "This command requires a non-empty selector.")
        }
        return selector
    }

    private static func requiredPostcondition(_ postcondition: Postcondition?) throws -> Postcondition {
        guard let postcondition else {
            throw HelperFailure(.invalidRequest, "This action requires a postcondition.")
        }
        return try postcondition.validated()
    }

    private static func awaitPostcondition(
        _ postcondition: Postcondition,
        context: ClaudeDesktopContext,
        limits: TraversalLimits
    ) throws {
        let postcondition = try postcondition.validated()
        let deadline = Date().addingTimeInterval(Double(postcondition.timeoutMilliseconds) / 1_000)
        repeat {
            let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
            if try postconditionHolds(postcondition, in: tree) { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        } while Date() < deadline
        throw HelperFailure(
            .postconditionFailed,
            "The required \(postcondition.condition.rawValue) postcondition did not become true."
        )
    }

    private static func postconditionHolds(_ postcondition: Postcondition, in tree: LiveTree) throws -> Bool {
        let matches = try ControlFinder.all(in: tree.snapshots, matching: postcondition.selector)
        if postcondition.condition == .absent { return matches.isEmpty }
        guard matches.count == 1,
              let live = tree.elements.first(where: { $0.snapshot.path == matches[0].path })
        else { return false }

        switch postcondition.condition {
        case .exists: return true
        case .absent: return false
        case .enabled: return live.snapshot.enabled == true
        case .disabled: return live.snapshot.enabled == false
        case .focused: return live.snapshot.focused == true
        case .valueEquals: return AXRuntime.valueAttribute(live.element) == postcondition.value
        }
    }
}
