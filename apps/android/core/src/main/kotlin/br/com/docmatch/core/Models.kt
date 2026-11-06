// The API's JSON (packages/contracts/openapi.yaml), as the app uses it. Unknown fields are ignored
// so a newer API never breaks an older app.
package br.com.docmatch.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/** JSON settings shared by the client and the tests. */
val DocmatchJson: Json = Json {
    ignoreUnknownKeys = true
    explicitNulls = false
    encodeDefaults = false
}

/** Which sign-ins this deployment offers (GET /v1/auth/methods). */
@Serializable
data class AuthMethods(
    val email: Boolean = false,
    val google: Boolean = false,
    val apple: Boolean = false,
    val password: Boolean = false,
    /** Local sign-in without any provider: only a development API offers it. */
    val dev: Boolean = false,
) {
    val any: Boolean get() = email || google || apple || password || dev
}

@Serializable
data class User(
    val id: String,
    val email: String,
    @SerialName("display_name") val displayName: String = "",
)

@Serializable
data class Org(val id: String, val name: String = "")

@Serializable
data class Credits(val balance: Long = 0, val reserved: Long = 0, val available: Long = 0, @SerialName("token_exempt") val tokenExempt: Boolean = false)

/** GET /v1/me: the account every client of the same person sees. */
@Serializable
data class Me(val user: User, val org: Org, val credits: Credits? = null)

/** The Session schema the sign-in endpoints answer with (the cookies carry the session). */
@Serializable
data class SessionInfo(val user: User, val org: Org)

@Serializable
data class EmailChallenge(
    @SerialName("challenge_id") val challengeId: String,
    @SerialName("expires_at") val expiresAt: String = "",
)

/** One signed-in browser or app install of the account (GET /v1/me/sessions). */
@Serializable
data class Device(
    val id: String,
    val platform: String,
    val name: String,
    @SerialName("created_at") val createdAt: String,
    @SerialName("last_seen_at") val lastSeenAt: String,
    @SerialName("ip_prefix") val ipPrefix: String? = null,
    val current: Boolean = false,
)

@Serializable
data class DeviceList(val items: List<Device> = emptyList())

/** DELETE /v1/me: when the account and its data go. */
@Serializable
data class DeletionScheduled(@SerialName("scheduled_for") val scheduledFor: String)

/** Presigned upload ticket: a form POST (documents) or a raw PUT (media). */
@Serializable
data class UploadTicket(
    val id: String,
    val method: String,
    val url: String,
    val fields: Map<String, String> = emptyMap(),
    val headers: Map<String, String> = emptyMap(),
)

/** openapi UploadStatus: `ready` passed the security scan. */
object UploadStatus {
    const val PENDING = "pending"
    const val SCANNING = "scanning"
    const val READY = "ready"
    const val REJECTED = "rejected"
    const val EXPIRED = "expired"
}

@Serializable
data class Upload(
    val id: String,
    @SerialName("file_name") val fileName: String = "",
    val status: String,
    @SerialName("rejected_reason") val rejectedReason: String? = null,
)

@Serializable
data class Quote(
    val id: String,
    @SerialName("expected_credits") val expectedCredits: Long,
    @SerialName("cap_credits") val capCredits: Long,
    val signature: String,
    @SerialName("requires_approval") val requiresApproval: Boolean = false,
    /** How long the run should take, queue included; absent on older quotes. */
    @SerialName("expected_seconds") val expectedSeconds: Long? = null,
    /** A large order: the confirmation says how long it takes and an e-mail follows. */
    val large: Boolean = false,
)

/** openapi RunStatus. */
object RunStatus {
    const val QUEUED = "queued"
    const val RUNNING = "running"
    const val NEEDS_APPROVAL = "needs_approval"
    const val COMPLETED = "completed"
    const val FAILED = "failed"
    const val CANCELLED = "cancelled"

    fun terminal(status: String): Boolean = status == COMPLETED || status == FAILED || status == CANCELLED

    /** Nothing changes without the user: finished, or parked until the extra estimate is approved. */
    fun settled(status: String): Boolean = terminal(status) || status == NEEDS_APPROVAL
}

/** The run options of the form, the same as the site's (rules text, "Usar regras", web search). */
data class RunOptions(val rulesText: String = "", val useRules: Boolean = true, val webSearch: Boolean = false)

@Serializable
data class RunProgress(val stage: String = "", val percent: Int = 0, val message: String? = null)

@Serializable
data class RunSummary(
    val reconciliation: ReconciliationDocument? = null,
    @SerialName("findings_total") val findingsTotal: Int = 0,
    @SerialName("amount_at_risk") val amountAtRisk: Long = 0,
    val currency: String = "BRL",
    @SerialName("lines_total") val linesTotal: Int = 0,
    @SerialName("lines_matched") val linesMatched: Int = 0,
    /** Per file, in pt-BR, what did not take part in the reconciliation. */
    @SerialName("source_warnings") val sourceWarnings: List<String> = emptyList(),
)

@Serializable
data class RunError(val code: String = "", val message: String = "")

@Serializable
data class Run(
    val id: String,
    val status: String,
    val sources: List<Upload> = emptyList(),
    val progress: RunProgress? = null,
    val summary: RunSummary? = null,
    val error: RunError? = null,
    /** The extra estimate a run parked at needs_approval waits for (POST /runs/{id}/approve-quote). */
    @SerialName("pending_quote") val pendingQuote: Quote? = null,
    @SerialName("created_at") val createdAt: String? = null,
    @SerialName("finished_at") val finishedAt: String? = null,
    @SerialName("project_id") val projectId: String? = null,
)

@Serializable
data class RunList(val items: List<Run> = emptyList(), @SerialName("next_cursor") val nextCursor: String? = null)

@Serializable
data class EvidenceValues(
    val date: String? = null,
    val description: String? = null,
    /** Centavos; negative for debits. */
    val amount: Long? = null,
    val document: String? = null,
)

@Serializable
data class EvidenceLocator(val row: Int? = null)

@Serializable
data class Evidence(
    @SerialName("source_id") val sourceId: String,
    val label: String? = null,
    val excerpt: String? = null,
    val values: EvidenceValues? = null,
    val locator: EvidenceLocator? = null,
)

@Serializable
data class Finding(
    val id: String,
    val code: String = "",
    val kind: String = "",
    val severity: String = "",
    /** Centavos. */
    @SerialName("amount_at_risk") val amountAtRisk: Long = 0,
    val currency: String = "BRL",
    val title: String,
    val explanation: String = "",
    val confidence: Double = 0.0,
    @SerialName("confidence_label") val confidenceLabel: String = "",
    val evidence: List<Evidence> = emptyList(),
    val status: String = "open",
)

@Serializable
data class FindingList(val items: List<Finding> = emptyList(), @SerialName("next_cursor") val nextCursor: String? = null)

/** openapi FindingFeedback statuses: the decisions the API records. */
object Decision {
    const val CONFIRMED = "confirmed"
    const val DISMISSED = "dismissed"
    const val RESOLVED = "resolved"
}

/** The error envelope of every 4xx/5xx answer. */
@Serializable
internal data class ErrorEnvelope(
    val code: String = "",
    val message: String = "",
    @SerialName("request_id") val requestId: String? = null,
)
