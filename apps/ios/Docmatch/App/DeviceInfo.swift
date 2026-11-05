import Foundation
import UIKit

/// What the User-Agent tells the API about this phone, so the account's device list reads
/// "App iOS · iPhone15,2" next to "App Android · Pixel 8" and "Firefox · Linux".
enum DeviceInfo {
    /// The hardware model identifier (the simulator reports the one it simulates).
    static var model: String {
        if let simulated = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] { return simulated }
        var info = utsname()
        uname(&info)
        return withUnsafeBytes(of: &info.machine) { raw in String(decoding: raw.prefix { $0 != 0 }, as: UTF8.self) }
    }

    @MainActor static var system: String { UIDevice.current.systemVersion }
}
