// Wiring for one install: the transport (cookies, User-Agent), the API, the flow and the texts.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public final class Docmatch: Sendable {
    public let texts: Texts
    public let cookies: CookieJar
    public let transport: Transport
    public let api: DocmatchAPI
    public let reconciler: Reconciler

    public var baseURL: URL { transport.base }

    public init(
        baseURL: URL, store: SecretStore, userAgent: String, textsJSON: Data, client: (any HTTPClient)? = nil,
        presigned: (any HTTPClient)? = nil, aliasLoopback: Bool = false,
        sleep: (@Sendable (UInt64) async throws -> Void)? = nil
    ) throws {
        texts = try Texts(json: textsJSON)
        cookies = CookieJar(store: store)
        transport = Transport(
            base: baseURL, cookies: cookies, userAgent: userAgent, texts: texts, api: client ?? URLSessionClient(timeout: 60),
            presigned: presigned ?? client ?? URLSessionClient(timeout: 300), aliasLoopback: aliasLoopback)
        api = DocmatchAPI(http: transport)
        if let sleep {
            reconciler = Reconciler(api: api, texts: texts, sleep: sleep)
        } else {
            reconciler = Reconciler(api: api, texts: texts)
        }
    }

    /// The User-Agent the API turns into "App iOS · <model>" in every device list.
    public static func iosUserAgent(version: String, model: String, system: String) -> String {
        "docmatch-ios/\(ascii(version)) (\(ascii(model)); iOS \(ascii(system)))"
    }

    /// Header values in printable ASCII; `;()` delimit the User-Agent's comment.
    static func ascii(_ s: String) -> String {
        let kept = s.unicodeScalars.map { $0.isASCII && $0.value >= 0x20 && $0.value <= 0x7e && !";()".unicodeScalars.contains($0) ? Character($0) : " " }
        return String(kept).split(separator: " ").joined(separator: " ")
    }
}
