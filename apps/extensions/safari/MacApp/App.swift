import SafariServices
import SwiftUI

@main
struct DocmatchSafari: App {
    @StateObject private var authorization = SafariAuthorization()
    var body: some Scene {
        Window("docmatch · Safari", id: "main") {
            VStack(alignment: .leading, spacing: 20) {
                Text(NativeTexts.text("native_title")).font(.title.bold())
                Text(authorization.status).accessibilityAddTraits(.updatesFrequently)
                Button(NativeTexts.text("native_settings")) {
                    guard let identifier = Bundle.main.object(forInfoDictionaryKey: "DocmatchExtensionID") as? String
                    else { return }
                    SFSafariApplication.showPreferencesForExtension(withIdentifier: identifier) { _ in
                        Task { @MainActor in authorization.status = NativeTexts.text("native_enable") }
                    }
                }
                Text(NativeTexts.text("native_scope")).foregroundStyle(.secondary)
            }
            .padding(32).frame(width: 440).fixedSize(horizontal: false, vertical: true)
            .onOpenURL { authorization.open($0) }
        }.windowResizability(.contentSize)
    }
}
