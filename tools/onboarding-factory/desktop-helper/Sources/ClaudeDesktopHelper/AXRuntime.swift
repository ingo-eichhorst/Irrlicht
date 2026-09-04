import AppKit
import ApplicationServices
import CoreGraphics
import DesktopHelperCore
import Foundation

struct LiveElement {
    let snapshot: ElementSnapshot
    let element: AXUIElement
}

struct LiveTree {
    let elements: [LiveElement]

    var snapshots: [ElementSnapshot] { elements.map(\.snapshot) }

    func unique(matching selector: ControlSelector) throws -> LiveElement {
        _ = try ControlFinder.unique(in: snapshots, matching: selector)
        return elements.first { ControlFinder.matches($0.snapshot, selector: selector) }!
    }
}

typealias AXAttributeReader = (AXUIElement, String) throws -> CFTypeRef?

enum AXRuntime {
    private static let manualAccessibilityAttribute = "AXManualAccessibility"
    private static let domIdentifierAttribute = "AXDOMIdentifier"
    private static let editableRoles: Set<String> = [
        kAXTextAreaRole as String,
        kAXTextFieldRole as String,
        kAXSearchFieldSubrole as String,
    ]

    static func readTree(
        application: AXUIElement,
        limits: TraversalLimits,
        childrenReader: (AXUIElement) throws -> [AXUIElement] = AXRuntime.children,
        attributeReader: @escaping AXAttributeReader = AXRuntime.attributeValue
    ) throws -> LiveTree {
        struct Pending {
            let element: AXUIElement
            let path: [Int]
            let hierarchy: [String]
            let depth: Int
        }

        let limits = try limits.validated()
        var pending = [Pending(element: application, path: [], hierarchy: [], depth: 0)]
        var cursor = 0
        var result: [LiveElement] = []

        while cursor < pending.count {
            guard result.count < limits.maxNodes else {
                throw HelperFailure(
                    .traversalLimit,
                    "Accessibility traversal exceeded maxNodes \(limits.maxNodes)."
                )
            }
            let item = pending[cursor]
            cursor += 1
            let snapshot = try snapshot(
                item.element,
                path: item.path,
                hierarchy: item.hierarchy,
                attributeReader: attributeReader
            )
            result.append(LiveElement(snapshot: snapshot, element: item.element))

            let children = try childrenReader(item.element)
            if !children.isEmpty, item.depth >= limits.maxDepth {
                throw HelperFailure(
                    .traversalLimit,
                    "Accessibility traversal exceeded maxDepth \(limits.maxDepth)."
                )
            }
            let childHierarchy = item.hierarchy + [snapshot.role]
            for (index, child) in children.enumerated() {
                pending.append(Pending(
                    element: child,
                    path: item.path + [index],
                    hierarchy: childHierarchy,
                    depth: item.depth + 1
                ))
            }
        }
        return LiveTree(elements: result)
    }

    /// Electron can defer its web Accessibility tree until an assistive client
    /// asks for it. This process-only AX flag exposes that tree. It does not
    /// activate a control or change Claude Desktop content.
    static func exposeElectronAccessibility(application: AXUIElement) throws {
        let status = AXUIElementSetAttributeValue(
            application,
            manualAccessibilityAttribute as CFString,
            kCFBooleanTrue
        )
        guard status == .success || status == .attributeUnsupported else {
            throw HelperFailure(
                .actionFailed,
                "Claude Desktop could not expose its Accessibility tree (AX error \(status.rawValue))."
            )
        }
    }

    static func snapshot(
        _ element: AXUIElement,
        path: [Int],
        hierarchy: [String],
        attributeReader: AXAttributeReader = AXRuntime.attributeValue
    ) throws -> ElementSnapshot {
        let role = try stringAttribute(
            element,
            kAXRoleAttribute,
            reader: attributeReader
        ) ?? "AXUnknown"
        return ElementSnapshot(
            path: path,
            role: role,
            subrole: try stringAttribute(element, kAXSubroleAttribute, reader: attributeReader),
            title: try stringAttribute(element, kAXTitleAttribute, reader: attributeReader),
            description: try stringAttribute(
                element,
                kAXDescriptionAttribute,
                reader: attributeReader
            ),
            identifier: try nonEmptyStringAttribute(
                element,
                kAXIdentifierAttribute,
                reader: attributeReader
            ) ?? nonEmptyStringAttribute(
                element,
                domIdentifierAttribute,
                reader: attributeReader
            ),
            enabled: try boolAttribute(element, kAXEnabledAttribute, reader: attributeReader),
            focused: try boolAttribute(element, kAXFocusedAttribute, reader: attributeReader),
            frame: try frame(of: element, reader: attributeReader),
            hierarchy: hierarchy,
            valueRedacted: editableRoles.contains(role)
        )
    }

    static func frame(
        of element: AXUIElement,
        reader: AXAttributeReader = AXRuntime.attributeValue
    ) throws -> Frame? {
        guard let position: CGPoint = try axValueAttribute(
            element,
            kAXPositionAttribute,
            type: .cgPoint,
            reader: reader
        ), let size: CGSize = try axValueAttribute(
            element,
            kAXSizeAttribute,
            type: .cgSize,
            reader: reader
        )
        else { return nil }
        return Frame(
            x: Double(position.x),
            y: Double(position.y),
            width: Double(size.width),
            height: Double(size.height)
        )
    }

    static func stringAttribute(
        _ element: AXUIElement,
        _ attribute: String,
        reader: AXAttributeReader = AXRuntime.attributeValue
    ) throws -> String? {
        guard let value = try reader(element, attribute) else { return nil }
        guard CFGetTypeID(value) == CFStringGetTypeID() else {
            throw invalidAttributeValue(attribute)
        }
        return value as? String
    }

    private static func nonEmptyStringAttribute(
        _ element: AXUIElement,
        _ attribute: String,
        reader: AXAttributeReader
    ) throws -> String? {
        guard let value = try stringAttribute(element, attribute, reader: reader),
              !value.isEmpty
        else { return nil }
        return value
    }

    static func boolAttribute(
        _ element: AXUIElement,
        _ attribute: String,
        reader: AXAttributeReader = AXRuntime.attributeValue
    ) throws -> Bool? {
        guard let value = try reader(element, attribute) else { return nil }
        guard CFGetTypeID(value) == CFBooleanGetTypeID() else {
            throw invalidAttributeValue(attribute)
        }
        return value as? Bool
    }

    static func valueAttribute(_ element: AXUIElement) throws -> String? {
        try stringAttribute(element, kAXValueAttribute)
    }

    static func isFocused(_ element: AXUIElement) throws -> Bool {
        try boolAttribute(element, kAXFocusedAttribute) == true
    }

    static func setValue(_ value: String, on element: AXUIElement) throws {
        let status = AXUIElementSetAttributeValue(element, kAXValueAttribute as CFString, value as CFTypeRef)
        guard status == .success else {
            throw HelperFailure(.actionFailed, "Accessibility refused the text value update (AX error \(status.rawValue)).")
        }
    }

    static func focus(_ element: AXUIElement) throws {
        let status = AXUIElementSetAttributeValue(
            element,
            kAXFocusedAttribute as CFString,
            kCFBooleanTrue
        )
        guard status == .success,
              try boolAttribute(element, kAXFocusedAttribute) == true
        else {
            throw HelperFailure(.actionFailed, "Accessibility could not focus the selected control.")
        }
    }

    static func postKeyboardEvent(keyCode: UInt16, modifiers: [String]) throws {
        var flags: CGEventFlags = []
        for modifier in modifiers {
            switch modifier {
            case "command": flags.insert(.maskCommand)
            case "control": flags.insert(.maskControl)
            case "option": flags.insert(.maskAlternate)
            case "shift": flags.insert(.maskShift)
            default:
                throw HelperFailure(.invalidRequest, "Unknown keyboard modifier \(modifier).")
            }
        }
        guard let down = CGEvent(keyboardEventSource: nil, virtualKey: CGKeyCode(keyCode), keyDown: true),
              let up = CGEvent(keyboardEventSource: nil, virtualKey: CGKeyCode(keyCode), keyDown: false)
        else {
            throw HelperFailure(.actionFailed, "Core Graphics could not create the keyboard event.")
        }
        down.flags = flags
        up.flags = flags
        down.post(tap: .cghidEventTap)
        up.post(tap: .cghidEventTap)
    }

    static func physicalClick(_ point: Point) throws {
        let location = CGPoint(x: point.x, y: point.y)
        guard let down = CGEvent(
            mouseEventSource: nil,
            mouseType: .leftMouseDown,
            mouseCursorPosition: location,
            mouseButton: .left
        ), let up = CGEvent(
            mouseEventSource: nil,
            mouseType: .leftMouseUp,
            mouseCursorPosition: location,
            mouseButton: .left
        ) else {
            throw HelperFailure(.actionFailed, "Core Graphics could not create the mouse event.")
        }
        down.post(tap: .cghidEventTap)
        up.post(tap: .cghidEventTap)
    }

    static func requireHitTarget(_ target: AXUIElement, at point: Point) throws {
        let system = AXUIElementCreateSystemWide()
        var hit: AXUIElement?
        let status = AXUIElementCopyElementAtPosition(
            system,
            Float(point.x),
            Float(point.y),
            &hit
        )
        guard status == .success, let hit, isSameOrDescendant(hit, of: target) else {
            throw HelperFailure(
                .staleControl,
                "The current click point does not hit the selected control."
            )
        }
    }

    static func children(of element: AXUIElement) throws -> [AXUIElement] {
        var names: CFArray?
        let namesStatus = AXUIElementCopyAttributeNames(element, &names)
        guard namesStatus == .success else {
            throw HelperFailure(
                .actionFailed,
                "Accessibility could not list attributes before reading AXChildren (AX error \(namesStatus.rawValue))."
            )
        }
        guard let names = names as? [String] else {
            throw HelperFailure(
                .actionFailed,
                "Accessibility returned an invalid attribute-name list."
            )
        }
        guard names.contains(kAXChildrenAttribute as String) else {
            return []
        }

        var value: CFTypeRef?
        let status = AXUIElementCopyAttributeValue(
            element,
            kAXChildrenAttribute as CFString,
            &value
        )
        return try decodeChildren(status: status, value: value)
    }

    static func decodeChildren(status: AXError, value: CFTypeRef?) throws -> [AXUIElement] {
        if status == .attributeUnsupported {
            return []
        }
        guard status == .success else {
            throw HelperFailure(
                .actionFailed,
                "Accessibility could not read AXChildren (AX error \(status.rawValue))."
            )
        }
        guard let value, CFGetTypeID(value) == CFArrayGetTypeID() else {
            throw HelperFailure(
                .actionFailed,
                "Accessibility returned an invalid AXChildren value."
            )
        }
        let array = value as! CFArray
        var children: [AXUIElement] = []
        children.reserveCapacity(CFArrayGetCount(array))
        for index in 0 ..< CFArrayGetCount(array) {
            guard let pointer = CFArrayGetValueAtIndex(array, index) else {
                throw HelperFailure(
                    .actionFailed,
                    "Accessibility returned an invalid AXChildren value."
                )
            }
            let childValue = Unmanaged<CFTypeRef>.fromOpaque(pointer).takeUnretainedValue()
            guard CFGetTypeID(childValue) == AXUIElementGetTypeID() else {
                throw HelperFailure(
                    .actionFailed,
                    "Accessibility returned an invalid AXChildren value."
                )
            }
            children.append(childValue as! AXUIElement)
        }
        return children
    }

    static func attributeValue(
        _ element: AXUIElement,
        _ attribute: String
    ) throws -> CFTypeRef? {
        var value: CFTypeRef?
        let status = AXUIElementCopyAttributeValue(
            element,
            attribute as CFString,
            &value
        )
        return try decodeAttribute(status: status, value: value, attribute: attribute)
    }

    static func decodeAttribute(
        status: AXError,
        value: CFTypeRef?,
        attribute: String
    ) throws -> CFTypeRef? {
        // AX reports noValue for a supported optional attribute that has no
        // current value. That is data absence, not a failed read.
        if status == .attributeUnsupported || status == .noValue {
            return nil
        }
        guard status == .success else {
            throw HelperFailure(
                .actionFailed,
                "Accessibility could not read \(attribute) (AX error \(status.rawValue))."
            )
        }
        guard let value else {
            throw invalidAttributeValue(attribute)
        }
        return value
    }

    private static func axValueAttribute<T>(
        _ element: AXUIElement,
        _ attribute: String,
        type: AXValueType,
        reader: AXAttributeReader
    ) throws -> T? {
        guard let raw = try reader(element, attribute) else { return nil }
        guard CFGetTypeID(raw) == AXValueGetTypeID() else {
            throw invalidAttributeValue(attribute)
        }
        let value = raw as! AXValue
        guard AXValueGetType(value) == type else {
            throw invalidAttributeValue(attribute)
        }
        let pointer = UnsafeMutablePointer<T>.allocate(capacity: 1)
        defer { pointer.deallocate() }
        guard AXValueGetValue(value, type, pointer) else {
            throw invalidAttributeValue(attribute)
        }
        return pointer.pointee
    }

    private static func invalidAttributeValue(_ attribute: String) -> HelperFailure {
        HelperFailure(
            .actionFailed,
            "Accessibility returned an invalid \(attribute) value."
        )
    }

    private static func isSameOrDescendant(_ candidate: AXUIElement, of target: AXUIElement) -> Bool {
        var current: AXUIElement? = candidate
        for _ in 0 ..< 32 {
            guard let element = current else { return false }
            if CFEqual(element, target) { return true }
            var parent: CFTypeRef?
            guard AXUIElementCopyAttributeValue(
                element,
                kAXParentAttribute as CFString,
                &parent
            ) == .success,
                let parent,
                CFGetTypeID(parent) == AXUIElementGetTypeID()
            else { return false }
            current = (parent as! AXUIElement)
        }
        return false
    }
}
