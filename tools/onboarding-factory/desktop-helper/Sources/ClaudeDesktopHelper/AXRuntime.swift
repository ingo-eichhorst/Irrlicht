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

enum AXRuntime {
    private static let manualAccessibilityAttribute = "AXManualAccessibility"
    private static let domIdentifierAttribute = "AXDOMIdentifier"
    private static let editableRoles: Set<String> = [
        kAXTextAreaRole as String,
        kAXTextFieldRole as String,
        kAXSearchFieldSubrole as String,
    ]

    static func readTree(application: AXUIElement, limits: TraversalLimits) throws -> LiveTree {
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
            let snapshot = snapshot(
                item.element,
                path: item.path,
                hierarchy: item.hierarchy
            )
            result.append(LiveElement(snapshot: snapshot, element: item.element))

            let children = children(of: item.element)
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
        hierarchy: [String]
    ) -> ElementSnapshot {
        let role = stringAttribute(element, kAXRoleAttribute) ?? "AXUnknown"
        return ElementSnapshot(
            path: path,
            role: role,
            subrole: stringAttribute(element, kAXSubroleAttribute),
            title: stringAttribute(element, kAXTitleAttribute),
            description: stringAttribute(element, kAXDescriptionAttribute),
            identifier: nonEmptyStringAttribute(element, kAXIdentifierAttribute)
                ?? nonEmptyStringAttribute(element, domIdentifierAttribute),
            enabled: boolAttribute(element, kAXEnabledAttribute),
            focused: boolAttribute(element, kAXFocusedAttribute),
            frame: frame(of: element),
            hierarchy: hierarchy,
            valueRedacted: editableRoles.contains(role)
        )
    }

    static func frame(of element: AXUIElement) -> Frame? {
        guard let position: CGPoint = axValueAttribute(element, kAXPositionAttribute, type: .cgPoint),
              let size: CGSize = axValueAttribute(element, kAXSizeAttribute, type: .cgSize)
        else { return nil }
        return Frame(
            x: Double(position.x),
            y: Double(position.y),
            width: Double(size.width),
            height: Double(size.height)
        )
    }

    static func stringAttribute(_ element: AXUIElement, _ attribute: String) -> String? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success else {
            return nil
        }
        return value as? String
    }

    private static func nonEmptyStringAttribute(_ element: AXUIElement, _ attribute: String) -> String? {
        guard let value = stringAttribute(element, attribute), !value.isEmpty else { return nil }
        return value
    }

    static func boolAttribute(_ element: AXUIElement, _ attribute: String) -> Bool? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success else {
            return nil
        }
        return value as? Bool
    }

    static func valueAttribute(_ element: AXUIElement) -> String? {
        stringAttribute(element, kAXValueAttribute)
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
        guard status == .success, boolAttribute(element, kAXFocusedAttribute) == true else {
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

    private static func children(of element: AXUIElement) -> [AXUIElement] {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, kAXChildrenAttribute as CFString, &value) == .success,
              let children = value as? [AXUIElement]
        else { return [] }
        return children
    }

    private static func axValueAttribute<T>(
        _ element: AXUIElement,
        _ attribute: String,
        type: AXValueType
    ) -> T? {
        var raw: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &raw) == .success,
              let raw,
              CFGetTypeID(raw) == AXValueGetTypeID()
        else { return nil }
        let value = raw as! AXValue
        guard AXValueGetType(value) == type else { return nil }
        let pointer = UnsafeMutablePointer<T>.allocate(capacity: 1)
        defer { pointer.deallocate() }
        return AXValueGetValue(value, type, pointer) ? pointer.pointee : nil
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
