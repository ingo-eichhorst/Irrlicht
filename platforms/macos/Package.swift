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
        )
    ]
)