import Foundation

extension DocmatchAPI {
    private func projectPath(_ id: String) -> String { "/v1/projects/\(Self.segment(id))" }
    /// Identity lookup still works after a company removes this membership.
    public func organizations() async throws -> AccountMemberships { try await http.raw.call("GET", "/v1/me/organizations", as: AccountMemberships.self) }
    public func projects(cursor: String? = nil, archived: Bool = false) async throws -> ProjectList {
        var query = [("archived", String(archived))]
        if let cursor { query.append(("cursor", cursor)) }
        return try await http.call("GET", "/v1/projects", as: ProjectList.self, query: query)
    }
    public func createProject(name: String) async throws -> SavedProject { try await http.call("POST", "/v1/projects", body: ["name": .string(name)], as: SavedProject.self) }
    public func project(_ id: String) async throws -> SavedProject { try await http.call("GET", projectPath(id), as: SavedProject.self) }
    public func updateProject(_ id: String, version: Int64, name: String? = nil, archived: Bool? = nil, settings: ProjectSettings? = nil) async throws -> SavedProject {
        var body: [String: JSONValue] = ["version": .int(version)]
        if let name { body["name"] = .string(name) }
        if let archived { body["archived"] = .bool(archived) }
        if let s = settings { body["settings"] = .object(["rules_text": .string(s.rulesText), "use_rules": .bool(s.useRules), "rule_ids": .array(s.ruleIds.map { .string($0) }), "llm_mode": .string(s.llmMode), "web_search": .bool(s.webSearch)]) }
        return try await http.call("PATCH", projectPath(id), body: body, as: SavedProject.self)
    }
    public func deleteProject(_ id: String, version: Int64) async throws {
        try await http.call("DELETE", projectPath(id), query: [("version", String(version))])
    }
    public func shareProject(_ id: String, version: Int64, visibility: String, users: [String]) async throws -> SavedProject {
        try await http.call("PUT", projectPath(id)+"/sharing", body: ["version": .int(version), "visibility": .string(visibility), "user_ids": .array(users.map { .string($0) })], as: SavedProject.self)
    }
    public func removeProjectDocument(_ id: String, document: String, version: Int64) async throws {
        try await http.call("DELETE", projectPath(id)+"/documents/"+Self.segment(document), query: [("version", String(version))])
    }
    public func replaceProjectDocument(_ id: String, document: String, replacement: String, version: Int64) async throws -> SavedProject {
        try await http.call("POST", projectPath(id)+"/documents/"+Self.segment(document)+"/replace", body: ["version": .int(version), "replacement_id": .string(replacement)], as: SavedProject.self)
    }
    public func projectActivity(_ id: String) async throws -> ProjectEventList { try await http.call("GET", projectPath(id)+"/activity", as: ProjectEventList.self) }
    public func activity(cursor: String? = nil) async throws -> RunList { try await http.call("GET", "/v1/projects/activity", as: RunList.self, query: cursor.map { [("cursor", $0)] } ?? []) }
    public func estimateProject(_ id: String, revision: Int64, reprocessReason: String = "") async throws -> ProjectAnalysis {
        try await http.call("POST", projectPath(id)+"/runs/estimate", body: ["revision": .int(revision), "reprocess_reason": .string(reprocessReason)], as: ProjectAnalysis.self)
    }
    /// Explicit cost approval only; the server reuses an unchanged completed run.
    public func reconcileProject(_ id: String, revision: Int64, quote: Quote?, idempotencyKey: String, reprocessReason: String = "") async throws -> ProjectAnalysis {
        var body: [String: JSONValue] = ["revision": .int(revision), "reprocess_reason": .string(reprocessReason)]
        if let quote { body["quote_id"] = .string(quote.id); body["quote_signature"] = .string(quote.signature) }
        return try await http.call("POST", projectPath(id)+"/runs", body: body, as: ProjectAnalysis.self, idempotencyKey: idempotencyKey)
    }
    public func team() async throws -> TeamInfo { try await http.call("GET", "/v1/team", as: TeamInfo.self) }
    public func convertToTeam(name: String) async throws -> TeamInfo { try await http.call("POST", "/v1/team", body: ["name": .string(name)], as: TeamInfo.self) }
    public func invite(email: String, role: String) async throws -> TeamInvitation { try await http.call("POST", "/v1/team/invitations", body: ["email": .string(email), "role": .string(role)], as: TeamInvitation.self) }
    public func resendInvitation(_ id: String) async throws { try await http.call("POST", "/v1/team/invitations/\(Self.segment(id))/resend") }
    public func revokeInvitation(_ id: String) async throws { try await http.call("DELETE", "/v1/team/invitations/\(Self.segment(id))") }
    public func changeMemberRole(_ id: String, role: String) async throws { try await http.call("PATCH", "/v1/team/members/\(Self.segment(id))", body: ["role": .string(role)]) }
    public func removeMember(_ id: String) async throws { try await http.call("DELETE", "/v1/team/members/\(Self.segment(id))") }
    public func transferOwnership(_ id: String) async throws { try await http.call("POST", "/v1/team/owner", body: ["user_id": .string(id)]) }
    public func leaveTeam() async throws { try await http.call("POST", "/v1/team/leave") }
    public func invitation(token: String) async throws -> InvitationPreview { try await http.raw.call("POST", "/v1/invitations/preview", body: ["token": .string(token)], as: InvitationPreview.self) }
    public func respondToInvitation(token: String, accept: Bool) async throws -> InvitationResponse { try await http.raw.call("POST", "/v1/invitations/respond", body: ["token": .string(token), "accept": .bool(accept)], as: InvitationResponse.self) }
}
