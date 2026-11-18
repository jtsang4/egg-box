// swift-tools-version: 6.0
import PackageDescription

// iOS only: the other platforms have no implementation, and the module's
// constructor says so there (errors.ErrUnsupported).
let package = Package(
    name: "Iosvpn",
    platforms: [.iOS(.v17)],
    products: [.library(name: "Iosvpn", type: .dynamic, targets: ["Iosvpn"])],
    dependencies: [],
    targets: [
        .target(name: "Iosvpn"),
    ],
    swiftLanguageModes: [.v5]
)
