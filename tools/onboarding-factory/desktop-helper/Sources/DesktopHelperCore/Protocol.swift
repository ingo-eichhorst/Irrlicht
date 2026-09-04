import Foundation

public enum HelperCommand: String, Codable, Sendable {
    case preflight
    case inspect
    case probe
    case setValue = "set_value"
    case keyboard
    case physicalClick = "physical_click"
}

public enum PostconditionKind: String, Codable, Sendable {
    case exists
    case absent
    case enabled
    case disabled
    case focused
    case valueEquals = "value_equals"
}

public struct Postcondition: Codable, Equatable, Sendable {
    public let selector: ControlSelector
    public let condition: PostconditionKind
    public let value: String?
    public let timeoutMilliseconds: Int

    public init(
        selector: ControlSelector,
        condition: PostconditionKind,
        value: String? = nil,
        timeoutMilliseconds: Int = 2_000
    ) {
        self.selector = selector
        self.condition = condition
        self.value = value
        self.timeoutMilliseconds = timeoutMilliseconds
    }

    public func validated() throws -> Postcondition {
        guard !selector.isEmpty else {
            throw HelperFailure(.invalidRequest, "A postcondition selector cannot be empty.")
        }
        guard (100 ... 10_000).contains(timeoutMilliseconds) else {
            throw HelperFailure(.invalidRequest, "A postcondition timeout must be 100...10000 milliseconds.")
        }
        if condition == .valueEquals, value == nil {
            throw HelperFailure(.invalidRequest, "A value_equals postcondition requires a value.")
        }
        return self
    }
}

public struct HelperRequest: Codable, Equatable, Sendable {
    public let protocolVersion: Int
    public let command: HelperCommand
    public let selector: ControlSelector?
    public let value: String?
    public let keyCode: UInt16?
    public let modifiers: [String]?
    public let postcondition: Postcondition?
    public let probes: [ProbeDefinition]?
    public let limits: TraversalLimits?

    public init(
        protocolVersion: Int,
        command: HelperCommand,
        selector: ControlSelector? = nil,
        value: String? = nil,
        keyCode: UInt16? = nil,
        modifiers: [String]? = nil,
        postcondition: Postcondition? = nil,
        probes: [ProbeDefinition]? = nil,
        limits: TraversalLimits? = nil
    ) {
        self.protocolVersion = protocolVersion
        self.command = command
        self.selector = selector
        self.value = value
        self.keyCode = keyCode
        self.modifiers = modifiers
        self.postcondition = postcondition
        self.probes = probes
        self.limits = limits
    }
}

public enum RequestValidator {
    private static let keyboardModifiers: Set<String> = ["command", "control", "option", "shift"]

    @discardableResult
    public static func validate(_ request: HelperRequest) throws -> TraversalLimits {
        guard request.protocolVersion == desktopHelperProtocolVersion else {
            throw HelperFailure(
                .unsupportedProtocol,
                "Unsupported protocolVersion \(request.protocolVersion). Expected \(desktopHelperProtocolVersion)."
            )
        }
        let limits = try (request.limits ?? TraversalLimits()).validated()
        switch request.command {
        case .preflight, .inspect:
            break
        case .probe:
            guard let probes = request.probes, !probes.isEmpty else {
                throw HelperFailure(.invalidRequest, "probe requires one or more named probe definitions.")
            }
            guard probes.allSatisfy({ !$0.name.isEmpty && !$0.selectors.isEmpty }) else {
                throw HelperFailure(.invalidRequest, "Each probe requires a name and at least one selector.")
            }
            guard Set(probes.map(\.name)).count == probes.count else {
                throw HelperFailure(.invalidRequest, "Probe names must be unique.")
            }
            for probe in probes {
                guard probe.selectors.allSatisfy({ !$0.isEmpty }) else {
                    throw HelperFailure(.invalidRequest, "A probe control selector cannot be empty.")
                }
            }
        case .setValue:
            try validateSelector(request.selector)
            guard request.value != nil else {
                throw HelperFailure(.invalidRequest, "set_value requires value.")
            }
        case .keyboard:
            try validateSelector(request.selector)
            guard let keyCode = request.keyCode, keyCode <= 127 else {
                throw HelperFailure(.invalidRequest, "keyboard requires keyCode in 0...127.")
            }
            let unknown = Set(request.modifiers ?? []).subtracting(keyboardModifiers)
            guard unknown.isEmpty else {
                throw HelperFailure(.invalidRequest, "keyboard contains an unknown modifier.")
            }
            guard let postcondition = request.postcondition else {
                throw HelperFailure(.invalidRequest, "This action requires a postcondition.")
            }
            _ = try postcondition.validated()
        case .physicalClick:
            try validateSelector(request.selector)
            guard let postcondition = request.postcondition else {
                throw HelperFailure(.invalidRequest, "This action requires a postcondition.")
            }
            _ = try postcondition.validated()
        }
        return limits
    }

    private static func validateSelector(_ selector: ControlSelector?) throws {
        guard let selector, !selector.isEmpty else {
            throw HelperFailure(.invalidRequest, "This command requires a non-empty selector.")
        }
    }
}

public struct ErrorPayload: Codable, Equatable, Sendable {
    public let code: FailureCode
    public let message: String

    public init(_ failure: HelperFailure) {
        code = failure.code
        message = failure.message
    }
}

public struct AppStatus: Codable, Equatable, Sendable {
    public let bundleIdentifier: String
    public let installed: Bool
    public let running: Bool
    public let accessibilityTrusted: Bool
    public let desktopVersion: String
    public let bundledClaudeCodeVersion: String

    public init(
        bundleIdentifier: String,
        installed: Bool,
        running: Bool,
        accessibilityTrusted: Bool,
        desktopVersion: String,
        bundledClaudeCodeVersion: String
    ) {
        self.bundleIdentifier = bundleIdentifier
        self.installed = installed
        self.running = running
        self.accessibilityTrusted = accessibilityTrusted
        self.desktopVersion = desktopVersion
        self.bundledClaudeCodeVersion = bundledClaudeCodeVersion
    }
}

public struct ActionResult: Codable, Equatable, Sendable {
    public let kind: String
    public let targetPath: [Int]
    public let valueRedacted: Bool
    public let postcondition: PostconditionKind

    public init(kind: String, targetPath: [Int], valueRedacted: Bool, postcondition: PostconditionKind) {
        self.kind = kind
        self.targetPath = targetPath
        self.valueRedacted = valueRedacted
        self.postcondition = postcondition
    }
}

public struct HelperResponse: Codable, Equatable, Sendable {
    public let protocolVersion: Int
    public let ok: Bool
    public let command: HelperCommand?
    public let status: AppStatus?
    public let elements: [ElementSnapshot]?
    public let controls: [ProbeMatch]?
    public let action: ActionResult?
    public let error: ErrorPayload?

    public init(
        ok: Bool,
        command: HelperCommand? = nil,
        status: AppStatus? = nil,
        elements: [ElementSnapshot]? = nil,
        controls: [ProbeMatch]? = nil,
        action: ActionResult? = nil,
        error: ErrorPayload? = nil
    ) {
        protocolVersion = desktopHelperProtocolVersion
        self.ok = ok
        self.command = command
        self.status = status
        self.elements = elements
        self.controls = controls
        self.action = action
        self.error = error
    }
}
