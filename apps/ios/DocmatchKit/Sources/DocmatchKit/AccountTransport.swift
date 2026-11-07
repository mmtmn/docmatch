// Immutable request destination; one shared transport owns cookies and the refresh lock.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

struct AccountTransport: Sendable {
    let raw: Transport
    let organizationID: String?
    let projectID: String?
    var cookies: CookieJar { raw.cookies }
    func message(_ key: String, _ vars: [String: any Sendable] = [:]) -> String { raw.message(key, vars) }
    func presignedURL(_ value: String) throws -> URL { try raw.presignedURL(value) }
    func sendPresigned(_ request: URLRequest, file: URL? = nil) async throws { try await raw.sendPresigned(request, file: file) }
    private func path(_ value: String) throws -> String {
        guard let projectID else { return value }
        guard projectID.range(of: "^prj_[A-Za-z0-9]+$", options: .regularExpression) != nil else {
            throw ApiError(status: 400, code: "validation_error", message: raw.message("errors.unknown"))
        }
        if value == "/v1/runs" || value.hasPrefix("/v1/runs/") || value == "/v1/uploads" || value.hasPrefix("/v1/uploads/") {
            return "/v1/projects/\(projectID)" + value.dropFirst(3)
        }
        return value
    }
    func call<T: Decodable & Sendable>(_ method: String, _ target: String, body: [String: JSONValue]? = nil, as type: T.Type, idempotent: Bool = false, query: [(String, String)] = [], idempotencyKey: String? = nil) async throws -> T {
        try await raw.call(method, path(target), body: body, as: type, idempotent: idempotent, query: query, idempotencyKey: idempotencyKey, organizationID: organizationID)
    }
    func call(_ method: String, _ target: String, body: [String: JSONValue]? = nil, idempotent: Bool = false, query: [(String, String)] = []) async throws {
        try await raw.call(method, path(target), body: body, idempotent: idempotent, query: query, organizationID: organizationID)
    }
    func bytes(_ target: String, query: [(String, String)]) async throws -> Data {
        try await raw.bytes(path(target), query: query, organizationID: organizationID)
    }
}
