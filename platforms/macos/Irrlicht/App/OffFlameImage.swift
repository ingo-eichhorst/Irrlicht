import AppKit

@MainActor
enum OffFlameImage {
    static let menuBar = build(pointSize: 18, config: .menuBar)
    static let overlaySlashed = build(pointSize: 24, config: .overlaySlashed)

    private struct Config {
        let bodyStops: String
        let slash: String
        let accessibilityDescription: String?

        static let menuBar = Config(
            bodyStops: """
            <stop offset="0%" stop-color="#f3f4f6" stop-opacity="0.95"/>
            <stop offset="100%" stop-color="#9ca3af" stop-opacity="0.95"/>
            """,
            slash: "",
            accessibilityDescription: "Irrlicht — no active sessions"
        )

        static let overlaySlashed = Config(
            bodyStops: """
            <stop offset="0%" stop-color="#6b7280" stop-opacity="0.6"/>
            <stop offset="100%" stop-color="#3f4551" stop-opacity="0.9"/>
            """,
            // Dark stroke under a lighter foreground stroke so the slash reads
            // against both the flame and the panel background. Coordinates and
            // stroke widths scaled to the 1254-unit viewBox of the new mark.
            slash: """
            <line x1="196" y1="156" x2="1058" y2="1136" stroke="#0a0f1a" stroke-width="137" stroke-linecap="round" opacity="0.9"/>
            <line x1="196" y1="156" x2="1058" y2="1136" stroke="#9ca3af" stroke-width="63" stroke-linecap="round"/>
            """,
            // Surrounding Text already announces the empty state.
            accessibilityDescription: nil
        )
    }

    private static func build(pointSize: CGFloat, config: Config) -> NSImage {
        let size = Int(pointSize.rounded())
        let svg = """
        <svg xmlns="http://www.w3.org/2000/svg" width="\(size)" height="\(size)" viewBox="0 0 1254 1254">
        <defs>
        <linearGradient id="wisp-off-body" x1="50%" y1="0%" x2="50%" y2="100%">
        \(config.bodyStops)
        </linearGradient>
        </defs>
        <path d="M584 157C609 199 615 250 609 308C604 362 581 412 526 464C474 513 424 562 392 625C356 695 349 790 372 869C410 1001 516 1065 635 1065C790 1065 888 956 888 785C888 718 858 654 800 615C791 609 787 612 788 624C789 644 795 661 794 683C792 743 755 784 697 784C654 784 625 751 624 699C623 661 644 621 670 581C706 526 734 484 734 416C734 321 680 246 598 167C593 162 588 158 584 157Z" fill="url(#wisp-off-body)"/>
        \(config.slash)
        </svg>
        """
        guard let data = svg.data(using: .utf8),
              let image = NSImage(data: data) else {
            return NSImage()
        }
        image.isTemplate = false
        image.size = NSSize(width: pointSize, height: pointSize)
        image.accessibilityDescription = config.accessibilityDescription
        return image
    }
}
