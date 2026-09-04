import DesktopHelperCore
import Foundation

struct CommandDependencies {
    let loadContext: () throws -> ClaudeDesktopContext
    let postKeyboardEvent: (UInt16, [String]) throws -> Void
    let physicalClick: (Point) throws -> Void

    static var live: CommandDependencies {
        CommandDependencies(
            loadContext: { try ClaudeDesktopContext.load() },
            postKeyboardEvent: AXRuntime.postKeyboardEvent,
            physicalClick: AXRuntime.physicalClick
        )
    }
}

enum ActionTransition {
    static func perform(
        condition: PostconditionKind,
        observe: () throws -> Bool,
        action: () throws -> Void,
        verify: () throws -> Void
    ) throws {
        try requireUnsatisfied(try observe(), condition: condition)
        try action()
        try verify()
    }

    static func requireUnsatisfied(
        _ isSatisfied: Bool,
        condition: PostconditionKind
    ) throws {
        guard !isSatisfied else {
            throw HelperFailure(
                .postconditionFailed,
                "The \(condition.rawValue) postcondition was already true before the action."
            )
        }
    }
}

enum KeyboardEventBoundary {
    static func emit(
        requireFrontmost: () throws -> Void,
        isTargetFocused: () -> Bool,
        postEvent: () throws -> Void
    ) throws {
        try requireFrontmost()
        guard isTargetFocused() else {
            throw HelperFailure(
                .actionFailed,
                "The selected control lost focus before the keyboard event."
            )
        }
        try postEvent()
    }
}

enum PostconditionObserver {
    static func holds(
        _ postcondition: Postcondition,
        in snapshots: [ElementSnapshot],
        valueAtPath: ([Int]) -> String? = { _ in nil }
    ) throws -> Bool {
        let matches = try ControlFinder.all(
            in: snapshots,
            matching: postcondition.selector
        )
        if matches.count > 1 {
            throw HelperFailure(
                .controlAmbiguous,
                "The postcondition selector matched \(matches.count) visible controls."
            )
        }
        if postcondition.condition == .absent {
            return matches.isEmpty
        }
        guard let match = matches.first else {
            return false
        }

        switch postcondition.condition {
        case .exists: return true
        case .absent: return false
        case .enabled: return match.enabled == true
        case .disabled: return match.enabled == false
        case .focused: return match.focused == true
        case .valueEquals: return valueAtPath(match.path) == postcondition.value
        }
    }
}

enum CommandRunner {
    static func run(
        _ request: HelperRequest,
        dependencies: CommandDependencies = .live
    ) throws -> HelperResponse {
        let limits = try RequestValidator.validate(request)
        let context = try dependencies.loadContext()

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
            return try keyboard(
                request,
                context: context,
                limits: limits,
                dependencies: dependencies
            )
        case .physicalClick:
            return try physicalClick(
                request,
                context: context,
                limits: limits,
                dependencies: dependencies
            )
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
        let postcondition = Postcondition(
            selector: selector,
            condition: .valueEquals,
            value: value
        )
        try performAction(
            postcondition,
            context: context,
            limits: limits,
            action: { try AXRuntime.setValue(value, on: target.element) }
        )
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
        limits: TraversalLimits,
        dependencies: CommandDependencies
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
        try performAction(
            postcondition,
            context: context,
            limits: limits,
            action: {
                try KeyboardEventBoundary.emit(
                    requireFrontmost: context.requireFrontmostForAction,
                    isTargetFocused: { AXRuntime.isFocused(target.element) },
                    postEvent: {
                        try dependencies.postKeyboardEvent(keyCode, request.modifiers ?? [])
                    }
                )
            }
        )
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
        limits: TraversalLimits,
        dependencies: CommandDependencies
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        let postcondition = try requiredPostcondition(request.postcondition)
        try context.requireFrontmostForAction()
        try AXRuntime.exposeElectronAccessibility(application: context.axApplication)
        let tree = try AXRuntime.readTree(application: context.axApplication, limits: limits)
        let target = try tree.unique(matching: selector)
        try performAction(
            postcondition,
            context: context,
            limits: limits,
            action: {
                // Re-read the selected AX object immediately before the click.
                // The request cannot supply a point. A stale object or invalid
                // frame fails.
                let fresh = AXRuntime.snapshot(
                    target.element,
                    path: target.snapshot.path,
                    hierarchy: target.snapshot.hierarchy
                )
                guard ControlFinder.matches(fresh, selector: selector),
                      let frame = fresh.frame
                else {
                    throw HelperFailure(
                        .staleControl,
                        "The selected control became stale before the click."
                    )
                }
                let plan = try ClickPlan(freshFrame: frame)
                try AXRuntime.requireHitTarget(target.element, at: plan.point)
                try context.requireFrontmostForAction()
                try dependencies.physicalClick(plan.point)
            }
        )
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

    private static func performAction(
        _ postcondition: Postcondition,
        context: ClaudeDesktopContext,
        limits: TraversalLimits,
        action: () throws -> Void
    ) throws {
        try ActionTransition.perform(
            condition: postcondition.condition,
            observe: {
                let tree = try AXRuntime.readTree(
                    application: context.axApplication,
                    limits: limits
                )
                return try postconditionHolds(postcondition, in: tree)
            },
            action: action,
            verify: {
                try awaitPostcondition(
                    postcondition,
                    context: context,
                    limits: limits
                )
            }
        )
    }

    private static func postconditionHolds(_ postcondition: Postcondition, in tree: LiveTree) throws -> Bool {
        try PostconditionObserver.holds(
            postcondition,
            in: tree.snapshots,
            valueAtPath: { path in
                guard let live = tree.elements.first(where: { $0.snapshot.path == path }) else {
                    return nil
                }
                return AXRuntime.valueAttribute(live.element)
            }
        )
    }
}
