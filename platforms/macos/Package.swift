// swift-tools-version: 5.9
let appVersion = "0.2.0"
import PackageDescription

let package = Package(
    name: "Irrlicht",
    platforms: [
        .macOS(.v13)
    ],
    products: [
        .executable(
            name: "Irrlicht",
            targets: ["Irrlicht"]
        )
    ],
    dependencies: [
        .package(url: "https://github.com/pointfreeco/swift-snapshot-testing", from: "1.17.0"),
    ],
    targets: [
        .executableTarget(
            name: "Irrlicht",
            dependencies: [],
            path: "Irrlicht",
            resources: [.copy("Resources")]
        ),
        .testTarget(
            name: "IrrlichtTests",
            dependencies: [
                "Irrlicht",
                .product(name: "SnapshotTesting", package: "swift-snapshot-testing"),
            ],
            path: "Tests"
        ),
        // Integration test harness that launches real apps and verifies focus.
        // All tests gate on TEST_HARNESS=1 — they are skipped in CI (no display).
        // Run manually with: TEST_HARNESS=1 swift test --filter LauncherTestHarness
        .testTarget(
            name: "LauncherTestHarness",
            dependencies: ["Irrlicht"],
            path: "TestsHarness"
        )
    ]
)
