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
        <linearGradient id="wisp-off-body" x1="50%" y1="100%" x2="50%" y2="0%">
        \(config.bodyStops)
        </linearGradient>
        </defs>
        <path d="M590 166L587 168L586 173L592 180L604 200L608 209L612 223L613 247L609 267L600 288L594 298L581 315L558 338L536 356L516 375L498 397L483 424L478 437L473 456L472 473L471 474L472 500L473 501L473 508L474 509L475 524L476 525L476 546L473 559L467 570L461 576L453 580L442 580L433 576L420 563L416 556L411 536L412 511L416 496L419 491L419 485L416 482L412 482L400 489L392 495L374 513L355 537L342 558L326 591L314 625L307 655L305 675L304 676L304 684L303 685L303 703L302 704L303 743L304 744L304 753L305 754L307 773L316 809L318 812L329 844L342 870L356 893L380 925L421 966L452 989L479 1005L509 1019L534 1028L561 1035L582 1038L583 1039L589 1039L590 1040L613 1041L614 1042L657 1041L658 1040L665 1040L666 1039L673 1039L685 1036L690 1036L718 1029L736 1023L773 1007L801 991L824 975L841 961L876 925L893 903L911 875L926 845L939 810L947 779L950 754L951 753L951 745L952 744L953 718L954 717L953 676L952 675L952 666L951 665L947 634L939 603L921 559L907 536L901 534L897 538L898 559L895 575L886 592L875 603L866 607L856 607L846 601L841 595L835 581L834 556L845 506L845 493L846 492L845 473L837 443L829 426L814 405L797 389L788 384L786 384L782 387L782 394L784 401L784 413L785 414L784 428L782 437L778 448L769 463L759 473L744 481L729 482L717 478L705 467L698 453L696 444L696 437L695 436L695 427L696 426L697 410L706 380L713 348L714 333L715 332L715 303L714 302L714 294L713 293L712 283L704 256L689 227L680 215L667 201L654 190L640 181L621 172L600 166ZM494 710L504 711L515 715L521 719L532 730L539 740L544 750L548 762L551 777L551 805L550 806L550 811L546 826L540 839L532 851L522 861L512 867L503 870L489 870L479 867L469 861L459 851L453 842L447 829L443 816L442 806L441 805L441 795L440 794L441 775L443 765L447 752L454 738L458 732L471 719L484 712ZM757 709L767 709L775 711L787 718L797 728L802 735L809 749L813 762L815 772L815 781L816 782L816 795L815 796L814 811L809 828L803 841L797 850L785 862L774 868L766 870L755 870L745 867L735 861L724 850L718 841L711 825L706 800L706 782L707 781L709 764L714 749L719 739L725 730L738 717L749 711Z" fill="url(#wisp-off-body)" fill-rule="evenodd"/>
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
