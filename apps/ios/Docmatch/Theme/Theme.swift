import SwiftUI
import UIKit

extension Color {
    /// A palette color of tokens.json that follows the system's light or dark appearance.
    init(_ token: DmColor) {
        self.init(
            uiColor: UIColor { traits in
                let c = traits.userInterfaceStyle == .dark ? token.dark : token.light
                return UIColor(red: c.red, green: c.green, blue: c.blue, alpha: 1)
            })
    }
}
