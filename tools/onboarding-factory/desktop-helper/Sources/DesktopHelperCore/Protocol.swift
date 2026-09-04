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

public enum StrictRequestDecoder {
    public static func decode(_ data: Data) throws -> HelperRequest {
        let object: Any
        do {
            object = try JSONSerialization.jsonObject(with: data)
        } catch {
            throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
        }
        guard let root = object as? [String: Any],
              let commandName = root["command"] as? String,
              let command = HelperCommand(rawValue: commandName)
        else {
            throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
        }

        try requireOnlyKeys(root, allowed: allowedTopLevelKeys(for: command))
        if let selector = root["selector"] {
            try validateSelectorObject(selector)
        }
        if let limits = root["limits"] {
            try requireObject(limits, keys: ["maxDepth", "maxNodes"])
        }
        if let postcondition = root["postcondition"] {
            guard let object = postcondition as? [String: Any] else {
                throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
            }
            try requireOnlyKeys(
                object,
                allowed: ["selector", "condition", "value", "timeoutMilliseconds"]
            )
            if let selector = object["selector"] {
                try validateSelectorObject(selector)
            }
        }
        if let probes = root["probes"] {
            guard let array = probes as? [Any] else {
                throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
            }
            for probe in array {
                guard let object = probe as? [String: Any] else {
                    throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
                }
                try requireOnlyKeys(
                    object,
                    allowed: ["name", "selectors", "required", "requiresGeometry"]
                )
                if let selectors = object["selectors"] {
                    guard let selectorArray = selectors as? [Any] else {
                        throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
                    }
                    for selector in selectorArray {
                        try validateSelectorObject(selector)
                    }
                }
            }
        }

        let request: HelperRequest
        do {
            request = try JSONDecoder().decode(HelperRequest.self, from: data)
        } catch {
            throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
        }
        _ = try RequestValidator.validate(request)
        return request
    }

    private static func allowedTopLevelKeys(for command: HelperCommand) -> Set<String> {
        let common: Set<String> = ["protocolVersion", "command"]
        switch command {
        case .preflight:
            return common
        case .inspect:
            return common.union(["limits"])
        case .probe:
            return common.union(["probes", "limits"])
        case .setValue:
            return common.union(["selector", "value", "limits"])
        case .keyboard:
            return common.union(["selector", "keyCode", "modifiers", "postcondition", "limits"])
        case .physicalClick:
            return common.union(["selector", "postcondition", "limits"])
        }
    }

    private static func validateSelectorObject(_ value: Any) throws {
        try requireObject(
            value,
            keys: ["role", "subrole", "title", "description", "identifier", "hierarchy"]
        )
    }

    private static func requireObject(_ value: Any, keys: Set<String>) throws {
        guard let object = value as? [String: Any] else {
            throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
        }
        try requireOnlyKeys(object, allowed: keys)
    }

    private static func requireOnlyKeys(
        _ object: [String: Any],
        allowed: Set<String>
    ) throws {
        guard Set(object.keys).isSubset(of: allowed) else {
            throw HelperFailure(
                .invalidRequest,
                "The JSON request contains unknown or command-inapplicable fields."
            )
        }
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
        case .preflight:
            try rejectFields(
                request,
                selector: false,
                value: false,
                keyCode: false,
                modifiers: false,
                postcondition: false,
                probes: false,
                limits: false
            )
        case .inspect:
            try rejectFields(
                request,
                selector: false,
                value: false,
                keyCode: false,
                modifiers: false,
                postcondition: false,
                probes: false,
                limits: true
            )
        case .probe:
            try rejectFields(
                request,
                selector: false,
                value: false,
                keyCode: false,
                modifiers: false,
                postcondition: false,
                probes: true,
                limits: true
            )
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
            try rejectFields(
                request,
                selector: true,
                value: true,
                keyCode: false,
                modifiers: false,
                postcondition: false,
                probes: false,
                limits: true
            )
            try validateSelector(request.selector)
            guard request.value != nil else {
                throw HelperFailure(.invalidRequest, "set_value requires value.")
            }
        case .keyboard:
            try rejectFields(
                request,
                selector: true,
                value: false,
                keyCode: true,
                modifiers: true,
                postcondition: true,
                probes: false,
                limits: true
            )
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
            try rejectFields(
                request,
                selector: true,
                value: false,
                keyCode: false,
                modifiers: false,
                postcondition: true,
                probes: false,
                limits: true
            )
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

    private static func rejectFields(
        _ request: HelperRequest,
        selector: Bool,
        value: Bool,
        keyCode: Bool,
        modifiers: Bool,
        postcondition: Bool,
        probes: Bool,
        limits: Bool
    ) throws {
        let invalid = (!selector && request.selector != nil)
            || (!value && request.value != nil)
            || (!keyCode && request.keyCode != nil)
            || (!modifiers && request.modifiers != nil)
            || (!postcondition && request.postcondition != nil)
            || (!probes && request.probes != nil)
            || (!limits && request.limits != nil)
        guard !invalid else {
            throw HelperFailure(
                .invalidRequest,
                "\(request.command.rawValue) contains fields that are not valid for this command."
            )
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
