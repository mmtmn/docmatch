import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import DocmatchKit

/// The real pt-BR texts (packages/locales/pt-BR.json), read from the repository.
func realTextsJSON() throws -> Data {
    var root = URL(fileURLWithPath: #filePath)
    for _ in 0..<6 { root.deleteLastPathComponent() }
    return try Data(contentsOf: root.appendingPathComponent("packages/locales/pt-BR.json"))
}

func realTexts() throws -> Texts { try Texts(json: realTextsJSON()) }

/// An API on a reserved name: nothing here ever leaves the process.
let apiBase = URL(string: "https://api.docmatch.invalid")!

/// A scripted server: answers in order (or through a handler) and records every request.
final class FakeServer: HTTPClient, @unchecked Sendable {
    struct Reply {
        var status: Int
        var body: Data
        var headers: [String: String]
        var delayMilliseconds: UInt64 = 0
    }

    private let lock = NSLock()
    private var queue: [Reply] = []
    private var recorded: [URLRequest] = []
    private var handler: (@Sendable (URLRequest) -> Reply?)?
    var failNetwork = false

    func enqueue(_ status: Int, _ json: String = "", setCookie: [String] = [], headers: [String: String] = [:], delayMilliseconds: UInt64 = 0) {
        var h = headers
        if !json.isEmpty { h["Content-Type"] = "application/json" }
        // Foundation joins repeated Set-Cookie headers with ", "; so does this fake.
        if !setCookie.isEmpty { h["Set-Cookie"] = setCookie.joined(separator: ", ") }
        lock.withLock { queue.append(Reply(status: status, body: Data(json.utf8), headers: h, delayMilliseconds: delayMilliseconds)) }
    }

    func route(_ handler: @escaping @Sendable (URLRequest) -> Reply?) { lock.withLock { self.handler = handler } }

    var requests: [URLRequest] { lock.withLock { recorded } }

    func take() -> URLRequest {
        lock.withLock { recorded.removeFirst() }
    }

    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let reply: Reply? = lock.withLock {
            recorded.append(request)
            if failNetwork { return nil }
            if let handler, let r = handler(request) { return r }
            return queue.isEmpty ? Reply(status: 599, body: Data(), headers: [:]) : queue.removeFirst()
        }
        guard let reply else { throw URLError(.notConnectedToInternet) }
        if reply.delayMilliseconds > 0 { try await Task.sleep(nanoseconds: reply.delayMilliseconds * 1_000_000) }
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1", headerFields: reply.headers)!
        return (reply.body, response)
    }
}

func sessionCookie(_ value: String = "s1.secret") -> String { "dm_session=\(value); Path=/; Max-Age=2592000; HttpOnly; Secure" }
func refreshCookie(_ value: String = "s1.refresh") -> String { "dm_refresh=\(value); Path=/v1/auth/refresh; Max-Age=2592000; HttpOnly; Secure" }
func deviceCookie(_ value: String = "dev-1") -> String { "dm_device=\(value); Path=/; Max-Age=34560000; HttpOnly; Secure" }

let sessionJSON = #"{"user":{"id":"usr_1","email":"ana@example.com","display_name":"Ana"},"org":{"id":"org_1","name":"Ana"}}"#
let unauthenticated = #"{"code":"unauthenticated","message":"Faça login para continuar."}"#

func newClient(_ server: FakeServer, store: SecretStore = MemorySecretStore(), base: URL = apiBase, alias: Bool = false) throws -> Docmatch {
    try Docmatch(
        baseURL: base, store: store, userAgent: Docmatch.iosUserAgent(version: "1.0.0", model: "iPhone15,2", system: "18.1"),
        textsJSON: realTextsJSON(), client: server, presigned: server, aliasLoopback: alias, sleep: { _ in })
}

func fakeReply(_ status: Int, _ json: String, _ setCookie: [String] = []) -> FakeServer.Reply {
    var headers = ["Content-Type": "application/json"]
    if !setCookie.isEmpty { headers["Set-Cookie"] = setCookie.joined(separator: ", ") }
    return FakeServer.Reply(status: status, body: Data(json.utf8), headers: headers)
}

func header(_ request: URLRequest, _ name: String) -> String? { request.value(forHTTPHeaderField: name) }

func jsonBody(_ request: URLRequest) -> [String: Any] {
    guard let body = request.httpBody, let object = try? JSONSerialization.jsonObject(with: body) as? [String: Any] else { return [:] }
    return object
}

/// Money and number text with the non-breaking spaces some platforms use turned into spaces.
func plain(_ s: String) -> String { s.replacingOccurrences(of: "\u{00A0}", with: " ").replacingOccurrences(of: "\u{202F}", with: " ") }

let organizationsJSON = #"{"user_id":"usr_1","items":[{"id":"org_1","name":"Pessoal","kind":"personal","role":"owner","plan":"starter","capabilities":{"manage_account":true,"create_project":true,"reconcile":true,"read_results":true,"upload":true}}]}"#
