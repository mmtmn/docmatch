// The API's answers (packages/contracts/openapi.yaml), with the snake_case names spelled out.
// Missing optional fields fall back to the defaults the Android core uses, so both apps read a
// reply the same way.
import Foundation

extension KeyedDecodingContainer {
    fileprivate func value<T: Decodable>(_ key: Key, _ fallback: T) throws -> T {
        try decodeIfPresent(T.self, forKey: key) ?? fallback
    }
}

/// The sign-ins this deployment offers (GET /v1/auth/methods).
public struct AuthMethods: Decodable, Sendable, Equatable {
    public var email: Bool
    public var google: Bool
    public var apple: Bool
    public var password: Bool
    /// Local sign-in without any provider: only a development API offers it.
    public var dev: Bool

    public init(email: Bool = false, google: Bool = false, apple: Bool = false, password: Bool = false, dev: Bool = false) {
        self.email = email
        self.google = google
        self.apple = apple
        self.password = password
        self.dev = dev
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(
            email: try c.value(.email, false), google: try c.value(.google, false), apple: try c.value(.apple, false),
            password: try c.value(.password, false), dev: try c.value(.dev, false))
    }

    private enum CodingKeys: String, CodingKey { case email, google, apple, password, dev }

    public var any: Bool { email || google || apple || dev }
}

public struct User: Decodable, Sendable, Equatable {
    public let id: String
    public let email: String
    public let displayName: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        email = try c.value(.email, "")
        displayName = try c.value(.displayName, "")
    }

    private enum CodingKeys: String, CodingKey {
        case id, email
        case displayName = "display_name"
    }
}

public struct Org: Decodable, Sendable, Equatable {
    public let id: String
    public let name: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.value(.name, "")
    }

    private enum CodingKeys: String, CodingKey { case id, name }
}

public struct Credits: Decodable, Sendable, Equatable {
    public let balance: Int64
    public let reserved: Int64
    public let available: Int64

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        balance = try c.value(.balance, 0)
        reserved = try c.value(.reserved, 0)
        available = try c.value(.available, 0)
    }

    private enum CodingKeys: String, CodingKey { case balance, reserved, available }
}

/// GET /v1/me: the account every client of the same person sees.
public struct Me: Decodable, Sendable, Equatable {
    public let user: User
    public let org: Org
    public let credits: Credits?
}

/// The Session schema the sign-in endpoints answer with (the cookies carry the session).
public struct SessionInfo: Decodable, Sendable, Equatable {
    public let user: User
    public let org: Org
}

public struct EmailChallenge: Decodable, Sendable, Equatable {
    public let challengeId: String
    public let expiresAt: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        challengeId = try c.decode(String.self, forKey: .challengeId)
        expiresAt = try c.value(.expiresAt, "")
    }

    private enum CodingKeys: String, CodingKey {
        case challengeId = "challenge_id"
        case expiresAt = "expires_at"
    }
}

/// One signed-in browser or app install of the account (GET /v1/me/sessions).
public struct Device: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let platform: String
    public let name: String
    public let createdAt: String
    public let lastSeenAt: String
    public let ipPrefix: String?
    public let current: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        platform = try c.value(.platform, "")
        name = try c.value(.name, "")
        createdAt = try c.value(.createdAt, "")
        lastSeenAt = try c.value(.lastSeenAt, "")
        ipPrefix = try c.decodeIfPresent(String.self, forKey: .ipPrefix)
        current = try c.value(.current, false)
    }

    private enum CodingKeys: String, CodingKey {
        case id, platform, name, current
        case createdAt = "created_at"
        case lastSeenAt = "last_seen_at"
        case ipPrefix = "ip_prefix"
    }
}

struct DeviceList: Decodable {
    let items: [Device]

    init(from decoder: any Decoder) throws {
        items = try decoder.container(keyedBy: CodingKeys.self).value(.items, [])
    }

    private enum CodingKeys: String, CodingKey { case items }
}

/// Presigned upload ticket: a form POST (documents) or a raw PUT (media).
public struct UploadTicket: Decodable, Sendable, Equatable {
    public let id: String
    public let method: String
    public let url: String
    public let fields: [String: String]
    public let headers: [String: String]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        method = try c.decode(String.self, forKey: .method)
        url = try c.decode(String.self, forKey: .url)
        fields = try c.value(.fields, [:])
        headers = try c.value(.headers, [:])
    }

    // Explicit keys everywhere (no key strategy): S3's form fields (success_action_status...) are
    // dictionary keys and must reach the upload exactly as signed.
    private enum CodingKeys: String, CodingKey { case id, method, url, fields, headers }
}

/// openapi UploadStatus: `ready` passed the security scan.
public enum UploadStatus {
    public static let pending = "pending"
    public static let scanning = "scanning"
    public static let ready = "ready"
    public static let rejected = "rejected"
    public static let expired = "expired"
}

public struct Upload: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let fileName: String
    public let status: String
    public let rejectedReason: String?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        fileName = try c.value(.fileName, "")
        status = try c.decode(String.self, forKey: .status)
        rejectedReason = try c.decodeIfPresent(String.self, forKey: .rejectedReason)
    }

    private enum CodingKeys: String, CodingKey {
        case id, status
        case fileName = "file_name"
        case rejectedReason = "rejected_reason"
    }
}

public struct Quote: Decodable, Sendable, Equatable {
    public let id: String
    public let expectedCredits: Int64
    public let capCredits: Int64
    public let signature: String
    public let requiresApproval: Bool
    /// How long the run should take, queue included; nil on older quotes.
    public let expectedSeconds: Int64?
    /// A large order: the confirmation says how long it takes and an e-mail follows.
    public let large: Bool

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        expectedCredits = try c.decode(Int64.self, forKey: .expectedCredits)
        capCredits = try c.decode(Int64.self, forKey: .capCredits)
        signature = try c.decode(String.self, forKey: .signature)
        requiresApproval = try c.value(.requiresApproval, false)
        expectedSeconds = try c.decodeIfPresent(Int64.self, forKey: .expectedSeconds)
        large = try c.value(.large, false)
    }

    private enum CodingKeys: String, CodingKey {
        case id, signature, large
        case expectedCredits = "expected_credits"
        case capCredits = "cap_credits"
        case requiresApproval = "requires_approval"
        case expectedSeconds = "expected_seconds"
    }
}

/// openapi RunStatus.
public enum RunStatus {
    public static let queued = "queued"
    public static let running = "running"
    public static let needsApproval = "needs_approval"
    public static let completed = "completed"
    public static let failed = "failed"
    public static let cancelled = "cancelled"

    public static func terminal(_ status: String) -> Bool { status == completed || status == failed || status == cancelled }

    /// Nothing changes without the user: finished, or parked until the extra estimate is approved.
    public static func settled(_ status: String) -> Bool { terminal(status) || status == needsApproval }
}

/// The run options of the form, the same as the site's (rules text, "Usar regras", web search).
public struct RunOptions: Sendable, Equatable {
    public var rulesText: String
    public var useRules: Bool
    public var webSearch: Bool

    public init(rulesText: String = "", useRules: Bool = true, webSearch: Bool = false) {
        self.rulesText = rulesText
        self.useRules = useRules
        self.webSearch = webSearch
    }
}

public struct RunProgress: Decodable, Sendable, Equatable {
    public let stage: String
    public let percent: Int
    public let message: String?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        stage = try c.value(.stage, "")
        percent = try c.value(.percent, 0)
        message = try c.decodeIfPresent(String.self, forKey: .message)
    }

    private enum CodingKeys: String, CodingKey { case stage, percent, message }
}

public struct RunSummary: Decodable, Sendable, Equatable {
    public let findingsTotal: Int
    public let amountAtRisk: Int64
    public let currency: String
    /// Per file, in pt-BR, what did not take part in the reconciliation.
    public let sourceWarnings: [String]

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        findingsTotal = try c.value(.findingsTotal, 0)
        amountAtRisk = try c.value(.amountAtRisk, 0)
        currency = try c.value(.currency, "BRL")
        sourceWarnings = try c.value(.sourceWarnings, [])
    }

    private enum CodingKeys: String, CodingKey {
        case currency
        case findingsTotal = "findings_total"
        case amountAtRisk = "amount_at_risk"
        case sourceWarnings = "source_warnings"
    }
}

public struct RunError: Decodable, Sendable, Equatable {
    public let code: String
    public let message: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        code = try c.value(.code, "")
        message = try c.value(.message, "")
    }

    private enum CodingKeys: String, CodingKey { case code, message }
}

public struct Run: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let status: String
    public let sources: [Upload]
    public let progress: RunProgress?
    public let summary: RunSummary?
    public let error: RunError?
    /// The extra estimate a run parked at needs_approval waits for (POST /runs/{id}/approve-quote).
    public let pendingQuote: Quote?
    public let createdAt: String?
    public let finishedAt: String?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        status = try c.decode(String.self, forKey: .status)
        sources = try c.value(.sources, [])
        progress = try c.decodeIfPresent(RunProgress.self, forKey: .progress)
        summary = try c.decodeIfPresent(RunSummary.self, forKey: .summary)
        error = try c.decodeIfPresent(RunError.self, forKey: .error)
        pendingQuote = try c.decodeIfPresent(Quote.self, forKey: .pendingQuote)
        createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt)
        finishedAt = try c.decodeIfPresent(String.self, forKey: .finishedAt)
    }

    private enum CodingKeys: String, CodingKey {
        case id, status, sources, progress, summary, error
        case pendingQuote = "pending_quote"
        case createdAt = "created_at"
        case finishedAt = "finished_at"
    }
}

public struct RunList: Decodable, Sendable, Equatable {
    public let items: [Run]
    public let nextCursor: String?

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        items = try c.value(.items, [])
        nextCursor = try c.decodeIfPresent(String.self, forKey: .nextCursor)
    }

    private enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }
}

public struct EvidenceValues: Decodable, Sendable, Equatable {
    public let date: String?
    public let description: String?
    /// Centavos; negative for debits.
    public let amount: Int64?
    public let document: String?
}

public struct Evidence: Decodable, Sendable, Equatable {
    public let sourceId: String
    public let label: String?
    public let excerpt: String?
    public let values: EvidenceValues?

    private enum CodingKeys: String, CodingKey {
        case label, excerpt, values
        case sourceId = "source_id"
    }
}

public struct Finding: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let code: String
    public let kind: String
    public let severity: String
    /// Centavos.
    public let amountAtRisk: Int64
    public let currency: String
    public let title: String
    public let explanation: String
    public let confidence: Double
    public let confidenceLabel: String
    public let evidence: [Evidence]
    public var status: String

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        code = try c.value(.code, "")
        kind = try c.value(.kind, "")
        severity = try c.value(.severity, "")
        amountAtRisk = try c.value(.amountAtRisk, 0)
        currency = try c.value(.currency, "BRL")
        title = try c.decode(String.self, forKey: .title)
        explanation = try c.value(.explanation, "")
        confidence = try c.value(.confidence, 0)
        confidenceLabel = try c.value(.confidenceLabel, "")
        evidence = try c.value(.evidence, [])
        status = try c.value(.status, "open")
    }

    private enum CodingKeys: String, CodingKey {
        case id, code, kind, severity, currency, title, explanation, confidence, evidence, status
        case amountAtRisk = "amount_at_risk"
        case confidenceLabel = "confidence_label"
    }
}

struct FindingList: Decodable {
    let items: [Finding]
    let nextCursor: String?

    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        items = try c.value(.items, [])
        nextCursor = try c.decodeIfPresent(String.self, forKey: .nextCursor)
    }

    private enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }
}

/// openapi FindingFeedback statuses: the decisions the API records.
public enum Decision {
    public static let confirmed = "confirmed"
    public static let dismissed = "dismissed"
    public static let resolved = "resolved"
}

/// The error envelope of every 4xx/5xx answer.
struct ErrorEnvelope: Decodable {
    let code: String?
    let message: String?
    let requestId: String?

    private enum CodingKeys: String, CodingKey {
        case code, message
        case requestId = "request_id"
    }
}
