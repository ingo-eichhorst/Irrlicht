// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "ClaudeDesktopHelper",
    platforms: [
        .macOS(.v13),
    ],
    products: [
        .executable(name: "claude-desktop-helper", targets: ["ClaudeDesktopHelper"]),
        .library(name: "DesktopHelperCore", targets: ["DesktopHelperCore"]),
    ],
    targets: [
        .target(name: "DesktopHelperCore"),
        .executableTarget(
            name: "ClaudeDesktopHelper",
            dependencies: ["DesktopHelperCore"]
        ),
        .testTarget(
            name: "DesktopHelperCoreTests",
            dependencies: ["DesktopHelperCore", "ClaudeDesktopHelper"],
            resources: [.copy("Fixtures")]
        ),
    ]
)
