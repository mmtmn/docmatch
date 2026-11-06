// HTTP to the docmatch API: JSON in and out, the {code, message, request_id} error envelope as
// ApiError with the server's pt-BR message, one silent refresh on `session_expired`, the end of a
// session revoked on another device, and the User-Agent the API turns into the device name
// (docmatch-ios/<version> (<model>; iOS <version>)).
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// An API error with the server's code and pt-BR message (or a local one for network failures).
public struct ApiError: Error, Sendable, Equatable, LocalizedError {
    public let status: Int
    public let code: String
    public let message: String
    public let requestId: String?

    public init(status: Int, code: String, message: String, requestId: String? = nil) {
        self.status = status
        self.code = code
        self.message = message
        self.requestId = requestId
    }

    public var errorDescription: String? { message }

    public static let network = "network"
    public static let uploadRejected = "upload_rejected"
    public static let uploadFailed = "upload_failed"
}

/// A JSON request body: Sendable, and encoded with sorted keys.
public enum JSONValue: Encodable, Sendable, Equatable {
    case string(String)
    case int(Int64)
    case bool(Bool)
    case array([JSONValue])
    case object([String: JSONValue])

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .string(let v): try c.encode(v)
        case .int(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .object(let v): try c.encode(v)
        }
    }
}

/// One request, one answer: URLSession in the app, a fake in the tests.
public protocol HTTPClient: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse)
    /// The request with a file as its body, streamed from disk (media can be gigabytes).
    func upload(_ request: URLRequest, fromFile file: URL) async throws -> (Data, HTTPURLResponse)
}

extension HTTPClient {
    public func upload(_ request: URLRequest, fromFile file: URL) async throws -> (Data, HTTPURLResponse) {
        var req = request
        req.httpBody = try Data(contentsOf: file, options: .mappedIfSafe)
        return try await send(req)
    }
}

/// URLSession without its own cookie storage or cache: the jar decides what is sent.
public final class URLSessionClient: HTTPClient, @unchecked Sendable {
    private let session: URLSession

    public init(timeout: TimeInterval) {
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.httpCookieAcceptPolicy = .never
        c.urlCache = nil
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.timeoutIntervalForRequest = timeout
        session = URLSession(configuration: c)
    }

    public func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return (data, http)
    }

    public func upload(_ request: URLRequest, fromFile file: URL) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await session.upload(for: request, fromFile: file)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return (data, http)
    }
}

/// An async lock: one refresh at a time, the others wait for it (an actor alone would let them
/// interleave at every await).
actor AsyncLock {
    private var locked = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func lock() async {
        if !locked {
            locked = true
            return
        }
        await withCheckedContinuation { waiters.append($0) }
    }

    func unlock() {
        if waiters.isEmpty {
            locked = false
        } else {
            waiters.removeFirst().resume()
        }
    }
}

/// Builds requests against one API origin with one cookie jar. `aliasLoopback` is for debug builds
/// only: the local API signs upload URLs for its own loopback, which a phone on the LAN reaches
/// through the API's host instead (the simulator shares the Mac's loopback and needs nothing).
public final class Transport: Sendable {
    public static let refreshPath = "/v1/auth/refresh"
    public static let sessionExpired = "session_expired"

    public let base: URL
    public let cookies: CookieJar
    private let userAgent: String
    private let texts: Texts
    private let api: any HTTPClient
    private let presigned: any HTTPClient
    private let aliasLoopback: Bool
    private let handlers = Locked<[@Sendable (ApiError) -> Void]>([])
    private let refreshing = AsyncLock()
    private let lastEnded = Locked<String?>(nil)

    public init(
        base: URL, cookies: CookieJar, userAgent: String, texts: Texts, api: any HTTPClient, presigned: any HTTPClient,
        aliasLoopback: Bool = false
    ) {
        self.base = base
        self.cookies = cookies
        self.userAgent = userAgent
        self.texts = texts
        self.api = api
        self.presigned = presigned
        self.aliasLoopback = aliasLoopback
    }

    /// Called when the API stops accepting this device's session: revoked from another device's
    /// account page, signed out everywhere, or past its lifetime. The cookies are already gone.
    public func onSessionEnded(_ handler: @escaping @Sendable (ApiError) -> Void) {
        handlers.mutate { $0.append(handler) }
    }

    /// A call answered with JSON. `idempotencyKey` makes a retry of the same operation safe (the
    /// run of one accepted estimate); `idempotent` asks for a fresh key, kept across the refresh.
    public func call<T: Decodable & Sendable>(
        _ method: String, _ path: String, body: [String: JSONValue]? = nil, as type: T.Type, idempotent: Bool = false,
        query: [(String, String)] = [], idempotencyKey: String? = nil, organizationID: String? = nil
    ) async throws -> T {
        let key = idempotencyKey ?? (idempotent ? UUID().uuidString.lowercased() : nil)
        let data = try await authed(path) { try await self.send(method, path, body: body, key: key, query: query, organizationID: organizationID) }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ApiError(status: 200, code: "server", message: texts.t("errors.unknown"))
        }
    }

    /// A call whose answer is not read (204s, feedback).
    public func call(_ method: String, _ path: String, body: [String: JSONValue]? = nil, idempotent: Bool = false, query: [(String, String)] = [], organizationID: String? = nil) async throws {
        let key = idempotent ? UUID().uuidString.lowercased() : nil
        _ = try await authed(path) { try await self.send(method, path, body: body, key: key, query: query, organizationID: organizationID) }
    }

    /// Raw bytes of a GET (exports).
    public func bytes(_ path: String, query: [(String, String)], organizationID: String? = nil) async throws -> Data {
        try await authed(path) { try await self.send("GET", path, body: nil, key: nil, query: query, accept: "*/*", organizationID: organizationID) }
    }

    /// A request to a presigned URL: never any cookie (the signature is the credential). With a
    /// file, the body is streamed from disk.
    public func sendPresigned(_ request: URLRequest, file: URL? = nil) async throws {
        var req = request
        req.setValue(userAgent, forHTTPHeaderField: "User-Agent")
        let (data, response): (Data, HTTPURLResponse)
        if let file {
            do {
                (data, response) = try await presigned.upload(req, fromFile: file)
            } catch {
                throw networkError(error)
            }
        } else {
            (data, response) = try await execute(presigned, req)
        }
        try check(data, response)
    }

    /// A pt-BR message of the shared texts.
    func message(_ key: String, _ vars: [String: any Sendable] = [:]) -> String { texts.t(key, vars) }

    /// A presigned URL from the API, with the loopback alias applied when enabled.
    public func presignedURL(_ raw: String) throws -> URL {
        guard var parts = URLComponents(string: raw), parts.url != nil else {
            throw ApiError(status: 0, code: ApiError.uploadFailed, message: texts.t("errors.unknown"))
        }
        if aliasLoopback, let host = parts.host, Transport.isLoopback(host), let apiHost = base.host, !Transport.isLoopback(apiHost) {
            parts.host = apiHost
        }
        return parts.url!
    }

    public func url(_ path: String, _ query: [(String, String)] = []) -> URL {
        var parts = URLComponents(url: base, resolvingAgainstBaseURL: false)!
        parts.percentEncodedPath = path.hasPrefix("/") ? path : "/" + path
        parts.queryItems = query.isEmpty ? nil : query.map { URLQueryItem(name: $0.0, value: $0.1) }
        return parts.url!
    }

    public static func isLoopback(_ host: String) -> Bool {
        let h = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        return h == "localhost" || h == "::1" || h.hasPrefix("127.")
    }

    // MARK: - internals

    /// The session rules of every call. A 401 for a session that another call already refreshed
    /// just retries; `session_expired` refreshes, one call at a time (a second refresh with the
    /// rotated-out secret would make the server revoke the whole session); any other 401 for the
    /// session this request carried means it is over, and this device forgets it too. Whether
    /// there was a session is read before the call: a refused refresh clears the cookies in its
    /// own answer.
    private func authed<T: Sendable>(_ path: String, _ attempt: () async throws -> T) async throws -> T {
        let sent = cookies.sessionValue()
        do {
            return try await attempt()
        } catch let e as ApiError {
            guard e.status == 401, path != Transport.refreshPath, let sent else { throw e }
            await refreshing.lock()
            do {
                let current = cookies.sessionValue()
                if current == nil {
                    throw e  // ended by another call, which announced it
                } else if current == sent {
                    guard e.code == Transport.sessionExpired else { throw end(e, sent) }
                    do {
                        _ = try await send("POST", Transport.refreshPath, body: nil, key: nil, query: [])
                    } catch let r as ApiError {
                        throw r.status == 401 ? end(r, sent) : r
                    }
                }  // else: refreshed by another call meanwhile, just retry
                await refreshing.unlock()
            } catch {
                await refreshing.unlock()
                throw error
            }
        }
        let retried = cookies.sessionValue()
        do {
            return try await attempt()
        } catch let e as ApiError {
            if e.status == 401, let retried, cookies.sessionValue() == retried { throw end(e, retried) }
            throw e
        }
    }

    /// Forgets the session (once per session value) and announces it.
    private func end(_ e: ApiError, _ session: String) -> ApiError {
        var first = false
        lastEnded.mutate {
            if $0 != session {
                $0 = session
                first = true
            }
        }
        guard first else { return e }
        cookies.clear()
        for handler in handlers.value { handler(e) }
        return e
    }

    private func send(
        _ method: String, _ path: String, body: [String: JSONValue]?, key: String?, query: [(String, String)],
        accept: String = "application/json", organizationID: String? = nil
    ) async throws -> Data {
        let target = url(path, query)
        var req = URLRequest(url: target)
        req.httpMethod = method
        req.setValue(accept, forHTTPHeaderField: "Accept")
        req.setValue(userAgent, forHTTPHeaderField: "User-Agent")
        if let body {
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
            req.httpBody = try encoder.encode(body)
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        } else if ["POST", "PUT", "PATCH"].contains(method) {
            req.httpBody = Data()
        }
        if let organizationID { req.setValue(organizationID, forHTTPHeaderField: "X-Docmatch-Org") }
        if let key { req.setValue(key, forHTTPHeaderField: "Idempotency-Key") }
        if let cookie = cookies.header(for: target) { req.setValue(cookie, forHTTPHeaderField: "Cookie") }
        let (data, response) = try await execute(api, req)
        // Errors carry cookies too (a refused refresh clears them), so they are read first.
        cookies.save(setCookie: Transport.setCookies(response), from: target)
        try check(data, response)
        return data
    }

    private func execute(_ client: any HTTPClient, _ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        do {
            return try await client.send(request)
        } catch {
            throw networkError(error)
        }
    }

    private func networkError(_ error: any Error) -> any Error {
        if let e = error as? ApiError { return e }
        if error is CancellationError || (error as? URLError)?.code == .cancelled || Task.isCancelled { return CancellationError() }
        return ApiError(status: 0, code: ApiError.network, message: texts.t("errors.network"))
    }

    private func check(_ data: Data, _ response: HTTPURLResponse) throws {
        guard !(200..<300).contains(response.statusCode) else { return }
        let envelope = try? JSONDecoder().decode(ErrorEnvelope.self, from: data)
        let code = envelope?.code.flatMap { $0.isEmpty ? nil : $0 } ?? "server"
        let message = envelope?.message.flatMap { $0.isEmpty ? nil : $0 } ?? texts.t("errors.server", ["status": response.statusCode])
        throw ApiError(status: response.statusCode, code: code, message: message, requestId: envelope?.requestId)
    }

    static func setCookies(_ response: HTTPURLResponse) -> [String] {
        var out: [String] = []
        for (key, value) in response.allHeaderFields {
            guard let k = key as? String, k.caseInsensitiveCompare("Set-Cookie") == .orderedSame, let v = value as? String else { continue }
            out += CookieJar.splitSetCookie(v)
        }
        return out
    }
}

/// A value behind a lock, for the few mutable things shared across tasks.
final class Locked<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: Value

    init(_ value: Value) { stored = value }

    var value: Value { lock.withLock { stored } }

    func mutate(_ body: (inout Value) -> Void) { lock.withLock { body(&stored) } }
}
