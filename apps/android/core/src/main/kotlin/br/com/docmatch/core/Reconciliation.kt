package br.com.docmatch.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class ReconciliationDocument(
    val version: Int,
    val title: String,
    val currency: String,
    @SerialName("period_start") val periodStart: String,
    @SerialName("period_end") val periodEnd: String,
    val receivables: List<ReconciledReceivable>,
    @SerialName("bank_entries") val bankEntries: List<ReconciledBankEntry>,
    val totals: ReconciliationTotals,
)

@Serializable
data class ReconciledReceivable(
    val id: String,
    @SerialName("source_id") val sourceId: String,
    @SerialName("source_name") val sourceName: String,
    @SerialName("source_row") val sourceRow: Int,
    val reference: String,
    val customer: String,
    @SerialName("original_amount") val originalAmount: Long,
    @SerialName("received_amount") val receivedAmount: Long,
    @SerialName("applied_principal") val appliedPrincipal: Long,
    @SerialName("open_amount") val openAmount: Long,
    @SerialName("proposed_amount") val proposedAmount: Long,
    @SerialName("withholding_pending") val withholdingPending: Long,
    val status: String,
    @SerialName("duplicate_of") val duplicateOf: String,
)

@Serializable
data class ReconciledBankEntry(
    val row: Int,
    val date: String,
    val description: String,
    val amount: Long,
    val kind: String,
    val proposed: Boolean,
    @SerialName("matched_ids") val matchedIds: List<String>,
    @SerialName("source_name") val sourceName: String,
)

@Serializable
data class ReconciliationTotals(
    @SerialName("original_amount") val originalAmount: Long,
    @SerialName("duplicate_excluded") val duplicateExcluded: Long,
    @SerialName("received_confirmed") val receivedConfirmed: Long,
    @SerialName("principal_confirmed") val principalConfirmed: Long,
    @SerialName("open_balance") val openBalance: Long,
    @SerialName("proposed_amount") val proposedAmount: Long,
    @SerialName("projected_open_balance") val projectedOpenBalance: Long,
    @SerialName("unapplied_cash") val unappliedCash: Long,
    @SerialName("bank_fees") val bankFees: Long,
    @SerialName("fines_received") val finesReceived: Long,
    @SerialName("withholding_pending") val withholdingPending: Long,
)

/** Severity precedes financial magnitude; informational totals can overlap. */
fun findingPriority(severity: String): Int = when (severity) { "critical" -> 0; "high" -> 1; "medium" -> 2; "low" -> 3; else -> 4 }
