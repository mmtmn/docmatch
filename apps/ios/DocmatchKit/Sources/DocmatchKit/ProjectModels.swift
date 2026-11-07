// Account and project responses from the shared API contract.
import Foundation

public struct AccountCapabilities: Decodable, Sendable, Equatable {
    public let manageAccount: Bool
    public let createProject: Bool
    public let reconcile: Bool
    public let readResults: Bool
    public let upload: Bool
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        manageAccount = try c.decodeIfPresent(Bool.self, forKey: .manageAccount) ?? false
        createProject = try c.decodeIfPresent(Bool.self, forKey: .createProject) ?? false
        reconcile = try c.decodeIfPresent(Bool.self, forKey: .reconcile) ?? false
        readResults = try c.decodeIfPresent(Bool.self, forKey: .readResults) ?? false
        upload = try c.decodeIfPresent(Bool.self, forKey: .upload) ?? false
    }
    private enum CodingKeys: String, CodingKey {
        case manageAccount = "manage_account"
        case createProject = "create_project"
        case reconcile
        case readResults = "read_results"
        case upload
    }
}

public struct AccountFeatures: Decodable, Sendable, Equatable {
    public let projects: Bool
    public let invitations: Bool
    public let extensions: Bool
}

public struct AccountMembership: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let name: String
    public let kind: String
    public let role: String
    public let plan: String
    public let capabilities: AccountCapabilities
    public let features: AccountFeatures?
    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case kind
        case role
        case plan
        case capabilities
        case features
    }
}

public struct AccountMemberships: Decodable, Sendable, Equatable {
    public let userId: String
    public let items: [AccountMembership]
    private enum CodingKeys: String, CodingKey {
        case userId = "user_id"
        case items
    }
}

public struct ProjectSettings: Codable, Sendable, Equatable {
    public var rulesText: String
    public var useRules: Bool
    public var ruleIds: [String]
    public var llmMode: String
    public var webSearch: Bool
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        rulesText = try c.decodeIfPresent(String.self, forKey: .rulesText) ?? ""
        useRules = try c.decodeIfPresent(Bool.self, forKey: .useRules) ?? true
        ruleIds = try c.decodeIfPresent([String].self, forKey: .ruleIds) ?? []
        llmMode = try c.decodeIfPresent(String.self, forKey: .llmMode) ?? ""
        webSearch = try c.decodeIfPresent(Bool.self, forKey: .webSearch) ?? false
    }
    private enum CodingKeys: String, CodingKey {
        case rulesText = "rules_text"
        case useRules = "use_rules"
        case ruleIds = "rule_ids"
        case llmMode = "llm_mode"
        case webSearch = "web_search"
    }
}

public struct SavedProject: Decodable, Sendable, Equatable, Identifiable {
    public let importedRunID: String?
    public let id: String
    public let name: String
    public let visibility: String
    public let version: Int64
    public let revision: Int64
    public let settings: ProjectSettings?
    public let documentCount: Int
    public let createdAt: String
    public let updatedAt: String
    public let archivedAt: String?
    public let capabilities: AccountCapabilities
    public let documents: [ProjectDocument]?
    public let sharedWith: [String]?
    private enum CodingKeys: String, CodingKey {
        case importedRunID = "imported_run_id"
        case id
        case name
        case visibility
        case version
        case revision
        case settings
        case documentCount = "document_count"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case archivedAt = "archived_at"
        case capabilities
        case documents
        case sharedWith = "shared_with"
    }
}

public struct ProjectDocument: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let uploadId: String
    public let replacesId: String?
    public let pendingReplacementId: String?
    public let fileName: String
    public let role: String
    public let status: String
    public let sizeBytes: Int64
    public let createdAt: String
    public let expiresAt: String?
    public let removedAt: String?
    private enum CodingKeys: String, CodingKey {
        case id
        case uploadId = "upload_id"
        case replacesId = "replaces_id"
        case pendingReplacementId = "pending_replacement_id"
        case fileName = "file_name"
        case role
        case status
        case sizeBytes = "size_bytes"
        case createdAt = "created_at"
        case expiresAt = "expires_at"
        case removedAt = "removed_at"
    }
}

public struct ProjectList: Decodable, Sendable, Equatable {
    public let items: [SavedProject]
    public let nextCursor: String?
    private enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }
}

public struct ProjectAnalysis: Decodable, Sendable, Equatable {
    public let historical: Bool?
    public let state: String
    public let revision: Int64
    public let run: Run?
    public let quote: Quote?
    private enum CodingKeys: String, CodingKey {
        case historical
        case state
        case revision
        case run
        case quote
    }
}

public struct ProjectEvent: Decodable, Sendable, Equatable, Identifiable {
    public let id: Int64
    public let action: String
    public let createdAt: String
    private enum CodingKeys: String, CodingKey {
        case id
        case action
        case createdAt = "created_at"
    }
}

public struct ProjectEventList: Decodable, Sendable, Equatable {
    public let items: [ProjectEvent]
    private enum CodingKeys: String, CodingKey {
        case items
    }
}

public struct TeamMember: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let name: String
    public let email: String
    public let role: String
    public let joinedAt: String
    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case email
        case role
        case joinedAt = "joined_at"
    }
}

public struct TeamInvitation: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let email: String
    public let role: String
    public let status: String
    public let expiresAt: String
    public let sentAt: String
    private enum CodingKeys: String, CodingKey {
        case id
        case email
        case role
        case status
        case expiresAt = "expires_at"
        case sentAt = "sent_at"
    }
}

public struct TeamInfo: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let name: String
    public let kind: String
    public let role: String
    public let capabilities: AccountCapabilities
    public let members: [TeamMember]
    public let invitations: [TeamInvitation]
    public let memberLimit: Int
    public let seatPrice: Int
    public let canInvite: Bool
    public let features: AccountFeatures?
    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case kind
        case role
        case capabilities
        case members
        case invitations
        case memberLimit = "member_limit"
        case seatPrice = "seat_price"
        case canInvite = "can_invite"
        case features
    }
}

public struct InvitationPreview: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let organization: String
    public let emailHint: String
    public let role: String
    public let status: String
    public let expiresAt: String
    public let emailMatches: Bool
    private enum CodingKeys: String, CodingKey {
        case id
        case organization
        case emailHint = "email_hint"
        case role
        case status
        case expiresAt = "expires_at"
        case emailMatches = "email_matches"
    }
}

public struct InvitationResponse: Decodable, Sendable, Equatable {
    public let organizationId: String
    public let accepted: Bool
    private enum CodingKeys: String, CodingKey {
        case organizationId = "organization_id"
        case accepted
    }
}


extension ProjectSettings {
    public init(rulesText: String = "", useRules: Bool = true, ruleIds: [String] = [], llmMode: String = "", webSearch: Bool = false) {
        self.rulesText = rulesText; self.useRules = useRules; self.ruleIds = ruleIds; self.llmMode = llmMode; self.webSearch = webSearch
    }
}

public struct ProjectRule: Decodable, Sendable, Equatable, Identifiable {
    public struct Provenance: Decodable, Sendable, Equatable { public let origin: String }
    public let id: String
    public let title: String
    public let body: String
    public let version: Int
    public let status: String
    public let provenance: Provenance
}
public struct ProjectRuleList: Decodable, Sendable, Equatable {
    public let items: [ProjectRule]
    public let nextCursor: String?
    private enum CodingKeys: String, CodingKey { case items; case nextCursor = "next_cursor" }
}

public struct InvitationInbox: Decodable, Sendable, Equatable {
    public let items: [InvitationPreview]
    public let nextCursor: String?
    private enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }
}
