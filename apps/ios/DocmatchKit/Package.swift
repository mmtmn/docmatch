// swift-tools-version:6.0
// DocmatchKit: everything in the iOS app that is not a screen. That is the API client, the cookie
// session, the pt-BR texts, the reconciliation flow and the screen models, so `swift test` covers
// it on Linux (make -C apps/ios ci runs it in Docker) as well as on a Mac.
import PackageDescription

let package = Package(
    name: "DocmatchKit",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [.library(name: "DocmatchKit", targets: ["DocmatchKit"])],
    targets: [
        .target(name: "DocmatchKit"),
        .testTarget(name: "DocmatchKitTests", dependencies: ["DocmatchKit"]),
    ],
    swiftLanguageModes: [.v6]
)
