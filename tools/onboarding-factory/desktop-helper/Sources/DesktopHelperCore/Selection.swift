import Foundation

public enum ControlFinder {
    public static func matches(_ element: ElementSnapshot, selector: ControlSelector) -> Bool {
        guard !selector.isEmpty else { return false }
        if let role = selector.role, element.role != role { return false }
        if let subrole = selector.subrole, element.subrole != subrole { return false }
        if let title = selector.title, element.title != title { return false }
        if let description = selector.description, element.description != description { return false }
        if let identifier = selector.identifier, element.identifier != identifier { return false }
        if let hierarchy = selector.hierarchy {
            guard element.hierarchy.count >= hierarchy.count else { return false }
            if Array(element.hierarchy.suffix(hierarchy.count)) != hierarchy { return false }
        }
        return true
    }

    public static func all(
        in elements: [ElementSnapshot],
        matching selector: ControlSelector
    ) throws -> [ElementSnapshot] {
        guard !selector.isEmpty else {
            throw HelperFailure(.invalidRequest, "A control selector must contain at least one attribute.")
        }
        return elements.filter { matches($0, selector: selector) }
    }

    public static func unique(
        in elements: [ElementSnapshot],
        matching selector: ControlSelector
    ) throws -> ElementSnapshot {
        let matches = try all(in: elements, matching: selector)
        switch matches.count {
        case 0:
            throw HelperFailure(.controlMissing, "The selector did not match a visible control.")
        case 1:
            return matches[0]
        default:
            throw HelperFailure(
                .controlAmbiguous,
                "The selector matched \(matches.count) visible controls. Add stable attributes or hierarchy."
            )
        }
    }
}

public struct ClickPlan: Equatable, Sendable {
    public let point: Point

    public init(freshFrame frame: Frame) throws {
        guard frame.x.isFinite, frame.y.isFinite, frame.width.isFinite, frame.height.isFinite,
              frame.width > 0, frame.height > 0
        else {
            throw HelperFailure(.staleControl, "The control has invalid current geometry.")
        }
        point = Point(x: frame.x + frame.width / 2, y: frame.y + frame.height / 2)
    }
}

public struct ProbeDefinition: Codable, Equatable, Sendable {
    public let name: String
    public let selectors: [ControlSelector]
    public let required: Bool
    public let requiresGeometry: Bool

    public init(
        name: String,
        selectors: [ControlSelector],
        required: Bool = true,
        requiresGeometry: Bool = true
    ) {
        self.name = name
        self.selectors = selectors
        self.required = required
        self.requiresGeometry = requiresGeometry
    }
}

public enum PreflightGate {
    public static func validate(installed: Bool, running: Bool, accessibilityTrusted: Bool) throws {
        guard installed else {
            throw HelperFailure(.appNotInstalled, "Claude Desktop is not installed.")
        }
        guard running else {
            throw HelperFailure(.appNotRunning, "Claude Desktop is installed but is not running.")
        }
        guard accessibilityTrusted else {
            throw HelperFailure(
                .accessibilityDenied,
                "macOS Accessibility access is not granted to claude-desktop-helper."
            )
        }
    }
}

public struct ProbeMatch: Codable, Equatable, Sendable {
    public let name: String
    public let visible: Bool
    public let path: [Int]?
    public let role: String?
    public let identifier: String?
    public let frame: Frame?

    public init(name: String, element: ElementSnapshot?) {
        self.name = name
        visible = element != nil
        path = element?.path
        role = element?.role
        identifier = element?.identifier
        frame = element?.frame
    }
}

public enum ProbeValidator {
    public static func validate(
        elements: [ElementSnapshot],
        definitions: [ProbeDefinition]
    ) throws -> [ProbeMatch] {
        try definitions.map { definition in
            let match = try uniqueMatch(for: definition, in: elements)
            try requirePresent(match, for: definition)
            try requireValidGeometry(match, for: definition)
            return ProbeMatch(name: definition.name, element: match)
        }
    }

    private static func uniqueMatch(
        for definition: ProbeDefinition,
        in elements: [ElementSnapshot]
    ) throws -> ElementSnapshot? {
        let allMatches = try definition.selectors.flatMap {
            try ControlFinder.all(in: elements, matching: $0)
        }
        let byPath = Dictionary(allMatches.map { ($0.path, $0) }) { first, _ in first }
        let matches = byPath.values.sorted {
            $0.path.lexicographicallyPrecedes($1.path)
        }
        guard matches.count <= 1 else {
            throw HelperFailure(
                .controlAmbiguous,
                "The \(definition.name) probe matched \(matches.count) visible controls."
            )
        }
        return matches.first
    }

    private static func requirePresent(
        _ match: ElementSnapshot?,
        for definition: ProbeDefinition
    ) throws {
        guard match != nil || !definition.required else {
            throw HelperFailure(
                .controlMissing,
                "The required \(definition.name) control is not visible."
            )
        }
    }

    private static func requireValidGeometry(
        _ match: ElementSnapshot?,
        for definition: ProbeDefinition
    ) throws {
        guard definition.requiresGeometry, let match else { return }
        do {
            _ = try ClickPlan(
                freshFrame: match.frame ?? Frame(x: .nan, y: .nan, width: 0, height: 0)
            )
        } catch {
            throw HelperFailure(
                .staleControl,
                "The \(definition.name) control has invalid current geometry."
            )
        }
    }
}

public struct SemanticVersion: Comparable, Equatable, Sendable {
    private let components: [Int]
    public let rawValue: String

    public init?(_ rawValue: String) {
        let main = rawValue.split(separator: "-", maxSplits: 1)[0]
        let parts = main.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count >= 3,
              parts.allSatisfy({ !$0.isEmpty && $0.allSatisfy(\.isNumber) })
        else { return nil }
        components = parts.map { Int($0)! }
        self.rawValue = rawValue
    }

    public static func < (lhs: SemanticVersion, rhs: SemanticVersion) -> Bool {
        let count = max(lhs.components.count, rhs.components.count)
        for index in 0 ..< count {
            let left = index < lhs.components.count ? lhs.components[index] : 0
            let right = index < rhs.components.count ? rhs.components[index] : 0
            if left != right { return left < right }
        }
        return lhs.rawValue < rhs.rawValue
    }
}
