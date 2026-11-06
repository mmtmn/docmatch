// Immutable engine output. Monetary values are integer centavos; review feedback never mutates it.
import Foundation

public struct ReconciliationDocument: Decodable, Sendable, Equatable {
    public let version: Int
    public let title: String
    public let currency: String
    public let periodStart: String
    public let periodEnd: String
    public let receivables: [ReconciledReceivable]
    public let bankEntries: [ReconciledBankEntry]
    public let totals: ReconciliationTotals

    private enum CodingKeys: String, CodingKey {
        case version
        case title
        case currency
        case periodStart = "period_start"
        case periodEnd = "period_end"
        case receivables
        case bankEntries = "bank_entries"
        case totals
    }
}

public struct ReconciledReceivable: Decodable, Sendable, Equatable {
    public let id: String
    public let sourceId: String
    public let sourceName: String
    public let sourceRow: Int
    public let reference: String
    public let customer: String
    public let originalAmount: Int64
    public let receivedAmount: Int64
    public let appliedPrincipal: Int64
    public let openAmount: Int64
    public let proposedAmount: Int64
    public let withholdingPending: Int64
    public let status: String
    public let duplicateOf: String

    private enum CodingKeys: String, CodingKey {
        case id
        case sourceId = "source_id"
        case sourceName = "source_name"
        case sourceRow = "source_row"
        case reference
        case customer
        case originalAmount = "original_amount"
        case receivedAmount = "received_amount"
        case appliedPrincipal = "applied_principal"
        case openAmount = "open_amount"
        case proposedAmount = "proposed_amount"
        case withholdingPending = "withholding_pending"
        case status
        case duplicateOf = "duplicate_of"
    }
}

public struct ReconciledBankEntry: Decodable, Sendable, Equatable {
    public let row: Int
    public let date: String
    public let description: String
    public let amount: Int64
    public let kind: String
    public let proposed: Bool
    public let matchedIds: [String]
    public let sourceName: String

    private enum CodingKeys: String, CodingKey {
        case row
        case date
        case description
        case amount
        case kind
        case proposed
        case matchedIds = "matched_ids"
        case sourceName = "source_name"
    }
}

public struct ReconciliationTotals: Decodable, Sendable, Equatable {
    public let originalAmount: Int64
    public let duplicateExcluded: Int64
    public let receivedConfirmed: Int64
    public let principalConfirmed: Int64
    public let openBalance: Int64
    public let proposedAmount: Int64
    public let projectedOpenBalance: Int64
    public let unappliedCash: Int64
    public let bankFees: Int64
    public let finesReceived: Int64
    public let withholdingPending: Int64

    private enum CodingKeys: String, CodingKey {
        case originalAmount = "original_amount"
        case duplicateExcluded = "duplicate_excluded"
        case receivedConfirmed = "received_confirmed"
        case principalConfirmed = "principal_confirmed"
        case openBalance = "open_balance"
        case proposedAmount = "proposed_amount"
        case projectedOpenBalance = "projected_open_balance"
        case unappliedCash = "unapplied_cash"
        case bankFees = "bank_fees"
        case finesReceived = "fines_received"
        case withholdingPending = "withholding_pending"
    }
}

public func findingPriority(_ severity: String) -> Int {
    switch severity { case "critical": 0; case "high": 1; case "medium": 2; case "low": 3; default: 4 }
}
