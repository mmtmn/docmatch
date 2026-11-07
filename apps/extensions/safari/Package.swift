// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "DocmatchSafariCore",
    platforms: [.macOS(.v14), .iOS(.v17)],
    products: [.library(name: "DocmatchSafariCore", targets: ["DocmatchSafariCore"])],
    targets: [
        .target(name: "DocmatchSafariCore"),
        .testTarget(name: "DocmatchSafariCoreTests", dependencies: ["DocmatchSafariCore"]),
    ]
)
