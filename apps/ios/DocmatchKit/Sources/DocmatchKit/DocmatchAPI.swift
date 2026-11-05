// Every API call the app makes, one method per route of packages/contracts/openapi.yaml: the same
// requests the Android core and the site send.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// A file picked on the phone, read into memory (documents are capped at tens of MB).
public struct LocalFile: Sendable, Equatable {
    public let name: String
    public let mime: String
    public let bytes: Data

    public init(name: String, mime: String, bytes: Data) {
        self.name = name
        self.mime = mime
        self.bytes = bytes
    }
}

public final class DocmatchAPI: Sendable {
    let http: Transport
    private static let maxPages = 50

    public init(http: Transport) { self.http = http }

    // MARK: - account and sign-in

    /// The sign-ins this deployment offers; none when the API cannot be reached.
    public func methods() async -> AuthMethods {
        (try? await http.call("GET", "/v1/auth/methods", as: AuthMethods.self)) ?? AuthMethods()
    }

    /// The signed-in account, or nil when this device has no valid session.
    public func me() async throws -> Me? {
        do {
            return try await http.call("GET", "/v1/me", as: Me.self)
        } catch let e as ApiError where e.status == 401 {
            return nil
        }
    }

    public func emailStart(_ email: String) async throws -> EmailChallenge {
        try await http.call("POST", "/v1/auth/email/start", body: ["email": .string(email)], as: EmailChallenge.self)
    }

    @discardableResult
    public func emailVerify(challengeId: String, code: String) async throws -> SessionInfo {
        try await http.call(
            "POST", "/v1/auth/email/verify", body: ["challenge_id": .string(challengeId), "code": .string(code)], as: SessionInfo.self)
    }

    /// Sign in with Apple on the phone: the identity token and the authorization code Apple gave
    /// the app (POST /v1/auth/apple/callback with JSON). The name only comes on the first sign-in.
    @discardableResult
    public func appleNative(identityToken: String, authorizationCode: String, fullName: String?) async throws -> SessionInfo {
        var body: [String: JSONValue] = ["identity_token": .string(identityToken), "authorization_code": .string(authorizationCode)]
        if let fullName, !fullName.isEmpty { body["full_name"] = .string(fullName) }
        return try await http.call("POST", "/v1/auth/apple/callback", body: body, as: SessionInfo.self)
    }

    /// The ID token of Google's own sign-in on the phone.
    @discardableResult
    public func googleNative(idToken: String) async throws -> SessionInfo {
        try await http.call("POST", "/v1/auth/google/native", body: ["id_token": .string(idToken)], as: SessionInfo.self)
    }

    /// Local sign-in (development API only).
    @discardableResult
    public func devLogin(email: String? = nil) async throws -> SessionInfo {
        var body: [String: JSONValue] = [:]
        if let email { body["email"] = .string(email) }
        return try await http.call("POST", "/v1/auth/dev/login", body: body, as: SessionInfo.self)
    }

    /// Signs this device out (the server revokes the session; the cookies go too).
    public func logout() async throws {
        defer { http.cookies.clear() }
        do {
            try await http.call("POST", "/v1/auth/logout")
        } catch let e as ApiError where e.status == 401 {
            return
        }
    }

    public func devices() async throws -> [Device] {
        try await http.call("GET", "/v1/me/sessions", as: DeviceList.self).items
    }

    /// Signs one device out; signing out this one also clears the local cookies.
    public func revokeDevice(_ device: Device) async throws {
        try await http.call("DELETE", "/v1/me/sessions/\(Self.segment(device.id))")
        if device.current { http.cookies.clear() }
    }

    // MARK: - uploads

    public func createUpload(_ file: LocalFile, sha256: String) async throws -> UploadTicket {
        try await http.call(
            "POST", "/v1/uploads",
            body: [
                "kind": .string("document"), "file_name": .string(file.name), "size_bytes": .int(Int64(file.bytes.count)),
                "mime": .string(file.mime), "sha256": .string(sha256),
            ],
            as: UploadTicket.self)
    }

    /// Sends the bytes to the presigned ticket (S3 in production, the API's local folder in dev).
    public func putObject(_ ticket: UploadTicket, _ file: LocalFile) async throws {
        var req = URLRequest(url: try http.presignedURL(ticket.url))
        if ticket.method.caseInsensitiveCompare("POST") == .orderedSame {
            let boundary = "docmatch-\(UUID().uuidString.lowercased())"
            req.httpMethod = "POST"
            req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
            req.httpBody = Self.multipart(fields: ticket.fields, file: file, boundary: boundary)
        } else {
            req.httpMethod = "PUT"
            req.setValue(file.mime, forHTTPHeaderField: "Content-Type")
            for (k, v) in ticket.headers { req.setValue(v, forHTTPHeaderField: k) }
            req.httpBody = file.bytes
        }
        try await http.sendPresigned(req)
    }

    public func completeUpload(_ id: String, sha256: String?) async throws -> Upload {
        var body: [String: JSONValue] = [:]
        if let sha256 { body["sha256"] = .string(sha256) }
        return try await http.call("POST", "/v1/uploads/\(Self.segment(id))/complete", body: body, as: Upload.self)
    }

    // MARK: - runs

    public func estimate(uploadIds: [String], rulesText: String) async throws -> Quote {
        try await http.call("POST", "/v1/runs/estimate", body: Self.runBody(uploadIds, rulesText), as: Quote.self)
    }

    public func createRun(_ quote: Quote, uploadIds: [String], rulesText: String) async throws -> Run {
        var body = Self.runBody(uploadIds, rulesText)
        body["quote_id"] = .string(quote.id)
        body["quote_signature"] = .string(quote.signature)
        return try await http.call("POST", "/v1/runs", body: body, as: Run.self, idempotent: true)
    }

    public func run(_ id: String) async throws -> Run {
        try await http.call("GET", "/v1/runs/\(Self.segment(id))", as: Run.self)
    }

    public func runs(cursor: String? = nil, limit: Int = 30) async throws -> RunList {
        var query = [("limit", String(limit))]
        if let cursor { query.append(("cursor", cursor)) }
        return try await http.call("GET", "/v1/runs", as: RunList.self, query: query)
    }

    /// Every finding of a run, page after page.
    public func findings(_ runId: String) async throws -> [Finding] {
        var out: [Finding] = []
        var cursor: String?
        for _ in 0..<Self.maxPages {
            var query = [("limit", "100")]
            if let cursor { query.append(("cursor", cursor)) }
            let page = try await http.call("GET", "/v1/runs/\(Self.segment(runId))/findings", as: FindingList.self, query: query)
            out += page.items
            guard let next = page.nextCursor, !next.isEmpty else { return out }
            cursor = next
        }
        return out
    }

    /// Records a decision (confirmed, dismissed, resolved) the site and the other apps then see.
    public func feedback(runId: String, findingId: String, decision: String, note: String? = nil) async throws {
        var body: [String: JSONValue] = ["status": .string(decision)]
        if let note, !note.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { body["note"] = .string(note) }
        try await http.call(
            "POST", "/v1/runs/\(Self.segment(runId))/findings/\(Self.segment(findingId))/feedback", body: body, idempotent: true)
    }

    /// The run's report as XLSX or PDF bytes.
    public func export(runId: String, format: String) async throws -> Data {
        try await http.bytes("/v1/runs/\(Self.segment(runId))/export", query: [("format", format)])
    }

    // MARK: - helpers

    static func runBody(_ uploadIds: [String], _ rulesText: String) -> [String: JSONValue] {
        var body: [String: JSONValue] = [
            "sources": .array(uploadIds.map { .object(["upload_id": .string($0)]) }),
            "options": .object(["use_rules": .bool(true)]),
        ]
        let rules = rulesText.trimmingCharacters(in: .whitespacesAndNewlines)
        if !rules.isEmpty { body["rules_text"] = .string(rules) }
        return body
    }

    /// A path segment, percent-encoded (ids are URL-safe already; this keeps them so).
    static func segment(_ s: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s
    }

    /// A multipart form with the policy fields first and the file last (S3 ignores what follows it).
    static func multipart(fields: [String: String], file: LocalFile, boundary: String) -> Data {
        var body = Data()
        func line(_ s: String) { body.append(Data((s + "\r\n").utf8)) }
        for (k, v) in fields.sorted(by: { $0.key < $1.key }) {
            line("--\(boundary)")
            line("Content-Disposition: form-data; name=\"\(quoted(k))\"")
            line("")
            line(v)
        }
        line("--\(boundary)")
        line("Content-Disposition: form-data; name=\"file\"; filename=\"\(quoted(file.name))\"")
        line("Content-Type: \(file.mime)")
        line("")
        body.append(file.bytes)
        line("")
        line("--\(boundary)--")
        return body
    }

    /// A form-data name or filename, escaped the way OkHttp does on Android.
    private static func quoted(_ s: String) -> String {
        s.replacingOccurrences(of: "\n", with: "%0A").replacingOccurrences(of: "\r", with: "%0D").replacingOccurrences(of: "\"", with: "%22")
    }
}
