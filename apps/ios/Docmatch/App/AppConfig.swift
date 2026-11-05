import DocmatchKit
import Foundation

/// What changes per build, from Info.plist (filled from build settings by project.yml): nothing
/// about the environment is written in the code.
struct AppConfig: Sendable {
    let apiBaseURL: URL
    let googleClientID: String
    let aliasLoopback: Bool
    let version: String

    static let shared = AppConfig.load()

    /// Google's own sign-in, when this build has an iOS client id.
    var google: GoogleOAuth? { GoogleOAuth(clientId: googleClientID) }

    private static func load() -> AppConfig {
        let info = Bundle.main.infoDictionary ?? [:]
        let raw = (info["DocmatchAPIBaseURL"] as? String ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: raw), let scheme = url.scheme, url.host != nil else {
            fatalError("DocmatchAPIBaseURL vazio: o build precisa de DOCMATCH_API_BASE_URL (make release lê do Terraform)")
        }
        #if !DEBUG
        // A release build only talks HTTPS; the local-network exception of Info.plist is for debug.
        precondition(scheme == "https", "o release só fala HTTPS com a API")
        #endif
        _ = scheme
        let alias = (info["DocmatchAliasLoopback"] as? String ?? "").uppercased() == "YES"
        return AppConfig(
            apiBaseURL: url,
            googleClientID: info["DocmatchGoogleClientID"] as? String ?? "",
            aliasLoopback: alias,
            version: info["CFBundleShortVersionString"] as? String ?? "0")
    }
}
