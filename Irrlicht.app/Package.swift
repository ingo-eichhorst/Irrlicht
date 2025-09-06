// swift-tools-version: 5.9
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
        // Add any external dependencies here if needed
    ],
    targets: [
        .executableTarget(
            name: "Irrlicht",
            dependencies: [],
            path: "Irrlicht"
        ),
        .testTarget(
            name: "IrrlichtTests",
            dependencies: ["Irrlicht"],
            path: "Tests"
        )
    ]
)