import Foundation

/// Only public, build-time destinations. The native app never receives session tokens or the PKCE verifier.
struct NativeConfiguration: Codable {
    let siteURL: URL
    let clientID: String
    let redirectURI: URL
    let local: Bool

    static func load(bundle: Bundle = .main) throws -> Self {
        guard let file = bundle.url(forResource: "docmatch-native", withExtension: "json") else {
            throw HandoffError.unavailable
        }
        let value = try JSONDecoder().decode(Self.self, from: Data(contentsOf: file))
        try value.validate()
        return value
    }

    func validate() throws {
        guard !clientID.isEmpty,
            let site = URLComponents(url: siteURL, resolvingAgainstBaseURL: false),
            site.user == nil, site.password == nil, site.query == nil, site.fragment == nil,
            site.path.isEmpty || site.path == "/",
            let callback = URLComponents(url: redirectURI, resolvingAgainstBaseURL: false),
            callback.host == "auth", callback.path.isEmpty, callback.port == nil,
            callback.user == nil, callback.password == nil, callback.query == nil, callback.fragment == nil
        else { throw HandoffError.invalid }
        if local {
            #if DEBUG
                guard site.scheme == "http", ["localhost", "127.0.0.1"].contains(site.host),
                    callback.scheme == "docmatch-extension-dev"
                else { throw HandoffError.invalid }
            #else
                throw HandoffError.invalid
            #endif
        } else {
            guard site.scheme == "https", let host = site.host, host.contains("."),
                !host.hasSuffix(".localhost"), !host.hasSuffix(".local"),
                callback.scheme == "br.com.docmatch.extension"
            else { throw HandoffError.invalid }
        }
    }

    func authorizationURL(for pending: Handoff) throws -> URL {
        var target = URLComponents(
            url: siteURL.appendingPathComponent("extensoes/autorizar"), resolvingAgainstBaseURL: false)!
        target.queryItems = [
            URLQueryItem(name: "client_id", value: clientID),
            URLQueryItem(name: "redirect_uri", value: redirectURI.absoluteString),
            URLQueryItem(name: "code_challenge", value: pending.challenge),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
            URLQueryItem(name: "state", value: pending.state),
        ]
        guard let url = target.url else { throw HandoffError.invalid }
        return url
    }

    func requestID(_ url: URL) throws -> UUID {
        guard let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
            parts.scheme == redirectURI.scheme, parts.host == "authorize", parts.path.isEmpty,
            parts.port == nil, parts.user == nil, parts.password == nil, parts.fragment == nil,
            parts.queryItems?.count == 1, parts.queryItems?.first?.name == "request",
            let raw = parts.queryItems?.first?.value, let id = UUID(uuidString: raw)
        else { throw HandoffError.invalid }
        return id
    }

    func store(bundle: Bundle = .main) throws -> HandoffStore {
        guard let group = bundle.object(forInfoDictionaryKey: "DocmatchAppGroup") as? String,
            !group.isEmpty, !group.contains("$(")
        else { throw HandoffError.unavailable }
        return try HandoffStore(group: group, callbackURL: redirectURI)
    }
}
