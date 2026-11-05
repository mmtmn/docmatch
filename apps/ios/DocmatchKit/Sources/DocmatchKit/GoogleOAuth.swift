// Google sign-in on iOS without Google's SDK: the installed-app OAuth flow with PKCE, opened in
// ASWebAuthenticationSession by the app. The ID token it ends with is issued to the iOS client id,
// which the API accepts (GOOGLE_OAUTH_NATIVE_CLIENT_IDS) and resolves like every other Google
// sign-in: Google subject, then verified e-mail. Pure Foundation, so the tests cover it.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public struct GoogleOAuth: Sendable {
    public static let authorizationEndpoint = URL(string: "https://accounts.google.com/o/oauth2/v2/auth")!
    public static let tokenEndpoint = URL(string: "https://oauth2.googleapis.com/token")!
    private static let clientSuffix = ".apps.googleusercontent.com"

    /// The iOS OAuth client id: <number>-<hash>.apps.googleusercontent.com.
    public let clientId: String
    /// Its reversed form, the scheme Google redirects to.
    public let callbackScheme: String

    public var redirectURI: String { "\(callbackScheme):/oauth2redirect" }

    /// Nil when the build has no (valid) iOS client id: the Google button stays hidden.
    public init?(clientId raw: String) {
        let id = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard id.hasSuffix(Self.clientSuffix) else { return nil }
        let prefix = id.dropLast(Self.clientSuffix.count)
        guard !prefix.isEmpty, prefix.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-") }) else { return nil }
        clientId = id
        callbackScheme = "com.googleusercontent.apps.\(prefix)"
    }

    /// One sign-in attempt: the PKCE verifier and the state stay in memory only.
    public struct Attempt: Sendable, Equatable {
        public let verifier: String
        public let state: String
        public let url: URL
    }

    public func begin() -> Attempt {
        let verifier = Self.random(32)
        let state = Self.random(16)
        var parts = URLComponents(url: Self.authorizationEndpoint, resolvingAgainstBaseURL: false)!
        parts.queryItems = [
            URLQueryItem(name: "client_id", value: clientId),
            URLQueryItem(name: "redirect_uri", value: redirectURI),
            URLQueryItem(name: "response_type", value: "code"),
            URLQueryItem(name: "scope", value: "openid email profile"),
            URLQueryItem(name: "code_challenge", value: Self.base64URL(Reconciler.sha256(Data(verifier.utf8)))),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
            URLQueryItem(name: "state", value: state),
            URLQueryItem(name: "prompt", value: "select_account"),
        ]
        return Attempt(verifier: verifier, state: state, url: parts.url!)
    }

    public enum Failure: Error, Equatable {
        case cancelled
        case refused(String)
        case stateMismatch
        case noToken
    }

    /// The authorization code of the callback, after checking it answers this attempt.
    public func code(from callback: URL, for attempt: Attempt) throws -> String {
        let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
        func item(_ name: String) -> String? { items.first { $0.name == name }?.value }
        if let error = item("error") { throw error == "access_denied" ? Failure.cancelled : Failure.refused(error) }
        guard item("state") == attempt.state else { throw Failure.stateMismatch }
        guard let code = item("code"), !code.isEmpty else { throw Failure.refused("no_code") }
        return code
    }

    /// The code exchange (a public client: PKCE instead of a secret).
    public func tokenRequest(code: String, for attempt: Attempt) -> URLRequest {
        var req = URLRequest(url: Self.tokenEndpoint)
        req.httpMethod = "POST"
        req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let form = [
            ("code", code), ("client_id", clientId), ("redirect_uri", redirectURI), ("grant_type", "authorization_code"),
            ("code_verifier", attempt.verifier),
        ]
        req.httpBody = Data(form.map { "\($0.0)=\(Self.formEncode($0.1))" }.joined(separator: "&").utf8)
        return req
    }

    /// The ID token of the token endpoint's answer.
    public static func idToken(from data: Data) throws -> String {
        struct Answer: Decodable {
            let idToken: String?
            private enum CodingKeys: String, CodingKey { case idToken = "id_token" }
        }
        guard let token = (try? JSONDecoder().decode(Answer.self, from: data))?.idToken, !token.isEmpty else { throw Failure.noToken }
        return token
    }

    static func random(_ count: Int) -> String {
        var generator = SystemRandomNumberGenerator()
        return base64URL((0..<count).map { _ in UInt8.random(in: 0...255, using: &generator) })
    }

    static func base64URL(_ bytes: [UInt8]) -> String {
        Data(bytes).base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    private static func formEncode(_ s: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s
    }
}
