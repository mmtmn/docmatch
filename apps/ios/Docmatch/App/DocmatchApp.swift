import DocmatchKit
import SwiftUI

/// The app: one Docmatch per process (API client, Keychain session, pt-BR texts) and the session
/// gate. Everything that is not a screen lives in DocmatchKit, tested on Linux and on a Mac.
@main
struct DocmatchApp: App {
    @State private var session: SessionModel
    private let texts: Texts

    init() {
        // Exports live only while a session does; a previous run may have left some behind.
        RunModel.clearExports()
        let config = AppConfig.shared
        let json = Bundle.main.url(forResource: "pt-BR", withExtension: "json").flatMap { try? Data(contentsOf: $0) } ?? Data("{}".utf8)
        let userAgent = Docmatch.iosUserAgent(version: config.version, model: DeviceInfo.model, system: DeviceInfo.system)
        do {
            let dm = try Docmatch(
                baseURL: config.apiBaseURL, store: KeychainSecretStore(), userAgent: userAgent, textsJSON: json,
                aliasLoopback: config.aliasLoopback)
            texts = dm.texts
            _session = State(initialValue: SessionModel(
                dm: dm, accountStore: KeychainSecretStore(account: "selected-account-v1")))
        } catch {
            fatalError("pt-BR.json inválido no pacote do app: \(error)")
        }
    }

    var body: some Scene {
        WindowGroup {
            RootView(session: session)
                .environment(\.texts, texts)
                .tint(Color(DmColors.brandPrimary))
        }
    }
}
