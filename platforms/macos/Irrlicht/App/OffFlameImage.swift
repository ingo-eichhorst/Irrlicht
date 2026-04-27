import AppKit

// Brand-mark "no sessions" flame, mirroring tools/irrlicht-design-system/assets/favicon-off.svg.
// Inline SVG → NSImage to match the pattern used in MenuBarStatusRenderer.
@MainActor
enum OffFlameImage {
    enum Variant {
        // Menu-bar idle: brighter, near-white gradient so the glyph reads
        // clearly at small sizes against any menu-bar background.
        case menuBar
        // Overlay empty state: dimmed grey gradient + diagonal slash, the
        // "no light" treatment paired with "No coding agent sessions detected".
        case overlaySlashed
    }

    static func image(pointSize: CGFloat, variant: Variant) -> NSImage {
        let size = Int(pointSize.rounded())
        let body: String
        let core: String
        let slash: String

        switch variant {
        case .menuBar:
            body = """
            <linearGradient id="wisp-off-body" x1="50%" y1="0%" x2="50%" y2="100%">
            <stop offset="0%" stop-color="#f3f4f6" stop-opacity="0.95"/>
            <stop offset="100%" stop-color="#9ca3af" stop-opacity="0.95"/>
            </linearGradient>
            """
            core = """
            <linearGradient id="wisp-off-core" x1="50%" y1="20%" x2="50%" y2="100%">
            <stop offset="0%" stop-color="#ffffff" stop-opacity="0.95"/>
            <stop offset="100%" stop-color="#e5e7eb" stop-opacity="0.7"/>
            </linearGradient>
            """
            slash = ""
        case .overlaySlashed:
            body = """
            <linearGradient id="wisp-off-body" x1="50%" y1="0%" x2="50%" y2="100%">
            <stop offset="0%" stop-color="#6b7280" stop-opacity="0.6"/>
            <stop offset="100%" stop-color="#3f4551" stop-opacity="0.9"/>
            </linearGradient>
            """
            core = """
            <linearGradient id="wisp-off-core" x1="50%" y1="20%" x2="50%" y2="100%">
            <stop offset="0%" stop-color="#9ca3af" stop-opacity="0.55"/>
            <stop offset="100%" stop-color="#6b7280" stop-opacity="0.3"/>
            </linearGradient>
            """
            // Diagonal slash, top-left to bottom-right, matching SF Symbol's
            // *.slash treatment. Drawn as a thicker dark stroke "shadow" plus
            // a lighter foreground stroke so it reads on both the flame and
            // the panel background.
            slash = """
            <line x1="5" y1="4" x2="27" y2="29" stroke="#0a0f1a" stroke-width="3.5" stroke-linecap="round" opacity="0.9"/>
            <line x1="5" y1="4" x2="27" y2="29" stroke="#9ca3af" stroke-width="1.6" stroke-linecap="round"/>
            """
        }

        let svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(size)" height="\(size)" viewBox="0 0 32 32">
        <defs>
        \(body)
        \(core)
        </defs>
        <path d="M 16.5 3 C 14.5 6, 12 8.5, 10.5 12 C 9 15.5, 9 19, 9.8 22 C 10.5 25, 12.5 27.5, 15 28.5 C 15.5 28.7, 16 28.8, 16.5 28.7 C 17.5 28.5, 19 28, 20.5 27 C 22.5 25.5, 23 22.5, 23 19.5 C 23 16, 22 13, 20.5 10.5 C 19 8, 17.5 5.5, 16.5 3 Z" fill="url(#wisp-off-body)"/>
        <path d="M 16 10 C 14.5 12, 13.5 14.5, 13.5 17.5 C 13.5 20.5, 14.5 23, 16 23.5 C 17.5 23, 18.5 20.5, 18.5 17.5 C 18.5 14.5, 17.5 12, 16 10 Z" fill="url(#wisp-off-core)"/>
        \(slash)
        </svg>
        """

        guard let data = svg.data(using: .utf8),
              let image = NSImage(data: data) else {
            return NSImage()
        }
        image.isTemplate = false
        image.size = NSSize(width: pointSize, height: pointSize)
        image.accessibilityDescription = "Irrlicht — no active sessions"
        return image
    }
}
