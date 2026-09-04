import ApplicationServices
import DesktopHelperCore
import Foundation

struct CommandContext {
    let status: AppStatus
    let application: AXUIElement
    let requireFrontmost: () throws -> Void
}

struct CommandDependencies {
    let loadContext: () throws -> CommandContext
    let exposeAccessibility: (AXUIElement) throws -> Void
    let readTree: (AXUIElement, TraversalLimits) throws -> LiveTree
    let setValue: (String, AXUIElement) throws -> Void
    let focus: (AXUIElement) throws -> Void
    let isFocused: (AXUIElement) throws -> Bool
    let valueAttribute: (AXUIElement) throws -> String?
    let snapshot: (AXUIElement, [Int], [String]) throws -> ElementSnapshot
    let requireHitTarget: (AXUIElement, Point) throws -> Void
    let postKeyboardEvent: (UInt16, [String]) throws -> Void
    let physicalClick: (Point) throws -> Void

    static var live: CommandDependencies {
        CommandDependencies(
            loadContext: {
                let context = try ClaudeDesktopContext.load()
                return CommandContext(
                    status: context.status,
                    application: context.axApplication,
                    requireFrontmost: context.requireFrontmostForAction
                )
            },
            exposeAccessibility: AXRuntime.exposeElectronAccessibility,
            readTree: { try AXRuntime.readTree(application: $0, limits: $1) },
            setValue: { try AXRuntime.setValue($0, on: $1) },
            focus: AXRuntime.focus,
            isFocused: AXRuntime.isFocused,
            valueAttribute: AXRuntime.valueAttribute,
            snapshot: { try AXRuntime.snapshot($0, path: $1, hierarchy: $2) },
            requireHitTarget: { try AXRuntime.requireHitTarget($0, at: $1) },
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
        isTargetFocused: () throws -> Bool,
        postEvent: () throws -> Void
    ) throws {
        try requireFrontmost()
        guard try isTargetFocused() else {
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
        valueAtPath: ([Int]) throws -> String? = { _ in nil }
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
        case .valueEquals: return try valueAtPath(match.path) == postcondition.value
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
            try dependencies.exposeAccessibility(context.application)
            let tree = try dependencies.readTree(context.application, limits)
            return HelperResponse(
                ok: true,
                command: request.command,
                status: context.status,
                elements: tree.snapshots
            )
        case .probe:
            let definitions = request.probes!
            try dependencies.exposeAccessibility(context.application)
            let tree = try dependencies.readTree(context.application, limits)
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
            return try setValue(
                request,
                context: context,
                limits: limits,
                dependencies: dependencies
            )
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
        context: CommandContext,
        limits: TraversalLimits,
        dependencies: CommandDependencies
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        guard let value = request.value else {
            throw HelperFailure(.invalidRequest, "set_value requires value.")
        }
        try context.requireFrontmost()
        try dependencies.exposeAccessibility(context.application)
        let tree = try dependencies.readTree(context.application, limits)
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
            dependencies: dependencies,
            action: { try dependencies.setValue(value, target.element) }
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
        context: CommandContext,
        limits: TraversalLimits,
        dependencies: CommandDependencies
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        guard let keyCode = request.keyCode, keyCode <= 127 else {
            throw HelperFailure(.invalidRequest, "keyboard requires keyCode in 0...127.")
        }
        let postcondition = try requiredPostcondition(request.postcondition)
        try context.requireFrontmost()
        try dependencies.exposeAccessibility(context.application)
        let tree = try dependencies.readTree(context.application, limits)
        let target = try tree.unique(matching: selector)
        try dependencies.focus(target.element)
        try performAction(
            postcondition,
            context: context,
            limits: limits,
            dependencies: dependencies,
            action: {
                try KeyboardEventBoundary.emit(
                    requireFrontmost: context.requireFrontmost,
                    isTargetFocused: { try dependencies.isFocused(target.element) },
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
        context: CommandContext,
        limits: TraversalLimits,
        dependencies: CommandDependencies
    ) throws -> HelperResponse {
        let selector = try requiredSelector(request.selector)
        let postcondition = try requiredPostcondition(request.postcondition)
        try context.requireFrontmost()
        try dependencies.exposeAccessibility(context.application)
        let tree = try dependencies.readTree(context.application, limits)
        let target = try tree.unique(matching: selector)
        try performAction(
            postcondition,
            context: context,
            limits: limits,
            dependencies: dependencies,
            action: {
                // Re-read the selected AX object immediately before the click.
                // The request cannot supply a point. A stale object or invalid
                // frame fails.
                let fresh = try dependencies.snapshot(
                    target.element,
                    target.snapshot.path,
                    target.snapshot.hierarchy
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
                try dependencies.requireHitTarget(target.element, plan.point)
                try context.requireFrontmost()
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
        context: CommandContext,
        limits: TraversalLimits,
        dependencies: CommandDependencies
    ) throws {
        let postcondition = try postcondition.validated()
        let deadline = Date().addingTimeInterval(Double(postcondition.timeoutMilliseconds) / 1_000)
        repeat {
            let tree = try dependencies.readTree(context.application, limits)
            if try postconditionHolds(
                postcondition,
                in: tree,
                dependencies: dependencies
            ) { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        } while Date() < deadline
        throw HelperFailure(
            .postconditionFailed,
            "The required \(postcondition.condition.rawValue) postcondition did not become true."
        )
    }

    private static func performAction(
        _ postcondition: Postcondition,
        context: CommandContext,
        limits: TraversalLimits,
        dependencies: CommandDependencies,
        action: () throws -> Void
    ) throws {
        try ActionTransition.perform(
            condition: postcondition.condition,
            observe: {
                let tree = try dependencies.readTree(context.application, limits)
                return try postconditionHolds(
                    postcondition,
                    in: tree,
                    dependencies: dependencies
                )
            },
            action: action,
            verify: {
                try awaitPostcondition(
                    postcondition,
                    context: context,
                    limits: limits,
                    dependencies: dependencies
                )
            }
        )
    }

    private static func postconditionHolds(
        _ postcondition: Postcondition,
        in tree: LiveTree,
        dependencies: CommandDependencies
    ) throws -> Bool {
        try PostconditionObserver.holds(
            postcondition,
            in: tree.snapshots,
            valueAtPath: { path in
                guard let live = tree.elements.first(where: { $0.snapshot.path == path }) else {
                    return nil
                }
                return try dependencies.valueAttribute(live.element)
            }
        )
    }
}
