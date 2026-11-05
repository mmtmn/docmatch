// The session cookies (dm_session, dm_refresh, dm_device), kept by the app itself rather than by
// URLSession, so they persist where the app decides (the Keychain, this device only) and behave the
// same on every platform: Foundation's own parser ignores Max-Age on Linux.
import Foundation

/// Where the jar keeps its bytes: the Keychain in the app, memory in the tests.
public protocol SecretStore: Sendable {
    func read() -> Data?
    func write(_ data: Data?)
}

public final class MemorySecretStore: SecretStore, @unchecked Sendable {
    private let lock = NSLock()
    private var data: Data?

    public init(_ data: Data? = nil) { self.data = data }

    public func read() -> Data? { lock.withLock { data } }

    public func write(_ data: Data?) { lock.withLock { self.data = data } }
}

struct StoredCookie: Codable, Equatable {
    var name: String
    var value: String
    var domain: String
    var path: String
    /// Milliseconds since 1970, like the Android jar.
    var expiresAt: Int64
    var secure: Bool
    var httpOnly: Bool
    var hostOnly: Bool

    func matches(_ url: URL) -> Bool {
        guard let host = url.host?.lowercased() else { return false }
        let domainOK = hostOnly ? host == domain : (host == domain || host.hasSuffix("." + domain))
        if !domainOK { return false }
        if secure && url.scheme?.lowercased() != "https" { return false }
        let requestPath = url.path.isEmpty ? "/" : url.path
        if requestPath == path { return true }
        guard requestPath.hasPrefix(path) else { return false }
        return path.hasSuffix("/") || requestPath.dropFirst(path.count).hasPrefix("/")
    }
}

/// A cookie jar persisted through a SecretStore: the next launch presents the same session, and a
/// refresh rotation replaces the stored cookie.
public final class CookieJar: @unchecked Sendable {
    public static let sessionCookie = "dm_session"

    private let store: SecretStore
    private let now: @Sendable () -> Date
    private let lock = NSLock()
    private var cookies: [StoredCookie]

    public init(store: SecretStore, now: @escaping @Sendable () -> Date = { Date() }) {
        self.store = store
        self.now = now
        let loaded = store.read().flatMap { try? JSONDecoder().decode([StoredCookie].self, from: $0) } ?? []
        let t = CookieJar.millis(now())
        cookies = loaded.filter { $0.expiresAt > t }
    }

    /// The Cookie header for a request to `url`, or nil when nothing applies.
    public func header(for url: URL) -> String? {
        lock.withLock {
            let t = CookieJar.millis(now())
            let before = cookies.count
            cookies.removeAll { $0.expiresAt <= t }
            if cookies.count != before { persist() }
            let matching = cookies.filter { $0.matches(url) }.sorted { $0.path.count > $1.path.count }
            return matching.isEmpty ? nil : matching.map { "\($0.name)=\($0.value)" }.joined(separator: "; ")
        }
    }

    /// Takes the Set-Cookie headers of a response from `url`.
    public func save(setCookie values: [String], from url: URL) {
        guard !values.isEmpty, let host = url.host?.lowercased() else { return }
        lock.withLock {
            let t = now()
            for raw in values {
                guard let c = CookieJar.parse(raw, host: host, requestPath: url.path, now: t) else { continue }
                cookies.removeAll { $0.name == c.name && $0.domain == c.domain && $0.path == c.path }
                if c.expiresAt > CookieJar.millis(t) { cookies.append(c) }
            }
            persist()
        }
    }

    /// Forgets every cookie (sign-out on this device).
    public func clear() {
        lock.withLock {
            cookies.removeAll()
            store.write(nil)
        }
    }

    /// Whether a session cookie is held (the account may still have revoked it server-side).
    public func hasSession() -> Bool {
        lock.withLock {
            let t = CookieJar.millis(now())
            return cookies.contains { $0.name == CookieJar.sessionCookie && $0.expiresAt > t }
        }
    }

    private func persist() {
        store.write(cookies.isEmpty ? nil : try? JSONEncoder().encode(cookies))
    }

    static func millis(_ date: Date) -> Int64 { Int64((date.timeIntervalSince1970 * 1000).rounded()) }

    /// One Set-Cookie value (RFC 6265 §5.2), or nil when it must be ignored.
    static func parse(_ raw: String, host: String, requestPath: String, now: Date) -> StoredCookie? {
        let parts = raw.split(separator: ";", omittingEmptySubsequences: false).map { $0.trimmingCharacters(in: .whitespaces) }
        guard let first = parts.first, let eq = first.firstIndex(of: "=") else { return nil }
        let name = String(first[..<eq]).trimmingCharacters(in: .whitespaces)
        let value = String(first[first.index(after: eq)...]).trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return nil }
        var domain: String?
        var path: String?
        var maxAge: Int64?
        var expires: Date?
        var secure = false
        var httpOnly = false
        for attr in parts.dropFirst() {
            let kv = attr.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            let key = kv[0].lowercased()
            let val = kv.count > 1 ? kv[1] : ""
            switch key {
            case "domain":
                let d = val.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "."))
                if !d.isEmpty { domain = d }
            case "path" where val.hasPrefix("/"): path = val
            case "max-age": if let n = Int64(val) { maxAge = n }
            case "expires": expires = httpDate(val)
            case "secure": secure = true
            case "httponly": httpOnly = true
            default: break
            }
        }
        if let d = domain, !(host == d || host.hasSuffix("." + d)) { return nil }  // a cookie for someone else
        let expiresAt: Int64
        if let maxAge {
            // RFC 6265bis caps a cookie's lifetime at 400 days.
            expiresAt = maxAge <= 0 ? 0 : millis(now) + min(maxAge, 400 * 24 * 3600) * 1000
        } else if let expires {
            expiresAt = millis(expires)
        } else {
            expiresAt = Int64.max  // a session cookie: kept until the server replaces or expires it
        }
        return StoredCookie(
            name: name, value: value, domain: domain ?? host, path: path ?? defaultPath(requestPath),
            expiresAt: expiresAt, secure: secure, httpOnly: httpOnly, hostOnly: domain == nil)
    }

    /// Foundation joins repeated Set-Cookie headers with ", "; split them back, minding the comma
    /// inside an Expires date ("Expires=Wed, 21 Oct 2026 07:28:00 GMT").
    public static func splitSetCookie(_ joined: String) -> [String] {
        var out: [String] = []
        for piece in joined.components(separatedBy: ",") {
            if let last = out.last, last.range(of: #"(?i)expires=\s*[a-z]{3}$"#, options: .regularExpression) != nil {
                out[out.count - 1] = last + "," + piece
            } else {
                out.append(piece.trimmingCharacters(in: .whitespaces))
            }
        }
        return out.filter { !$0.isEmpty }
    }

    private static func defaultPath(_ requestPath: String) -> String {
        guard requestPath.hasPrefix("/"), let slash = requestPath.lastIndex(of: "/"), slash != requestPath.startIndex else { return "/" }
        return String(requestPath[..<slash])
    }

    private static func httpDate(_ s: String) -> Date? {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "GMT")
        for pattern in ["EEE, dd MMM yyyy HH:mm:ss zzz", "EEEE, dd-MMM-yy HH:mm:ss zzz", "EEE MMM d HH:mm:ss yyyy"] {
            f.dateFormat = pattern
            if let d = f.date(from: s) { return d }
        }
        return nil
    }
}
