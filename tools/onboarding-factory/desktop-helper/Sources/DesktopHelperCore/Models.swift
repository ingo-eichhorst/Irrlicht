import Foundation

public let desktopHelperProtocolVersion = 1

public struct Frame: Codable, Equatable, Sendable {
    public let x: Double
    public let y: Double
    public let width: Double
    public let height: Double

    public init(x: Double, y: Double, width: Double, height: Double) {
        self.x = x
        self.y = y
        self.width = width
        self.height = height
    }
}

public struct Point: Codable, Equatable, Sendable {
    public let x: Double
    public let y: Double

    public init(x: Double, y: Double) {
        self.x = x
        self.y = y
    }
}

public struct ElementSnapshot: Codable, Equatable, Sendable {
    public let path: [Int]
    public let role: String
    public let subrole: String?
    public let title: String?
    public let description: String?
    public let identifier: String?
    public let enabled: Bool?
    public let focused: Bool?
    public let frame: Frame?
    public let hierarchy: [String]
    public let valueRedacted: Bool

    public init(
        path: [Int],
        role: String,
        subrole: String? = nil,
        title: String? = nil,
        description: String? = nil,
        identifier: String? = nil,
        enabled: Bool? = nil,
        focused: Bool? = nil,
        frame: Frame? = nil,
        hierarchy: [String] = [],
        valueRedacted: Bool = false
    ) {
        self.path = path
        self.role = role
        self.subrole = subrole
        self.title = title
        self.description = description
        self.identifier = identifier
        self.enabled = enabled
        self.focused = focused
        self.frame = frame
        self.hierarchy = hierarchy
        self.valueRedacted = valueRedacted
    }
}

public struct ControlSelector: Codable, Equatable, Sendable {
    public let role: String?
    public let subrole: String?
    public let title: String?
    public let description: String?
    public let identifier: String?
    public let hierarchy: [String]?

    public init(
        role: String? = nil,
        subrole: String? = nil,
        title: String? = nil,
        description: String? = nil,
        identifier: String? = nil,
        hierarchy: [String]? = nil
    ) {
        self.role = role
        self.subrole = subrole
        self.title = title
        self.description = description
        self.identifier = identifier
        self.hierarchy = hierarchy
    }

    public var isEmpty: Bool {
        role == nil && subrole == nil && title == nil && description == nil
            && identifier == nil && hierarchy == nil
    }
}

public enum FailureCode: String, Codable, Sendable {
    case invalidRequest = "invalid_request"
    case unsupportedProtocol = "unsupported_protocol"
    case appNotInstalled = "app_not_installed"
    case appNotRunning = "app_not_running"
    case accessibilityDenied = "accessibility_denied"
    case versionMetadataMissing = "version_metadata_missing"
    case traversalLimit = "traversal_limit"
    case controlMissing = "control_missing"
    case controlAmbiguous = "control_ambiguous"
    case staleControl = "stale_control"
    case actionFailed = "action_failed"
    case postconditionFailed = "postcondition_failed"
}

public struct HelperFailure: Error, Equatable, Sendable {
    public let code: FailureCode
    public let message: String

    public init(_ code: FailureCode, _ message: String) {
        self.code = code
        self.message = message
    }

    public var exitCode: Int32 {
        switch code {
        case .invalidRequest: 2
        case .unsupportedProtocol: 3
        case .appNotInstalled: 10
        case .appNotRunning: 11
        case .accessibilityDenied: 12
        case .versionMetadataMissing: 13
        case .traversalLimit: 20
        case .controlMissing: 21
        case .controlAmbiguous: 22
        case .staleControl: 23
        case .actionFailed: 24
        case .postconditionFailed: 25
        }
    }
}

public struct TraversalLimits: Codable, Equatable, Sendable {
    public let maxDepth: Int
    public let maxNodes: Int

    public init(maxDepth: Int = 14, maxNodes: Int = 5_000) {
        self.maxDepth = maxDepth
        self.maxNodes = maxNodes
    }

    public func validated() throws -> TraversalLimits {
        guard (1 ... 32).contains(maxDepth), (1 ... 50_000).contains(maxNodes) else {
            throw HelperFailure(
                .invalidRequest,
                "Traversal limits must use maxDepth 1...32 and maxNodes 1...50000."
            )
        }
        return self
    }
}
