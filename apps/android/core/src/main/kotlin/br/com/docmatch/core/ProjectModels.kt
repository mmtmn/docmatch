// Account and project responses from the shared API contract.
package br.com.docmatch.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class AccountCapabilities(
    @SerialName("manage_account")
    val manageAccount: Boolean = false,
    @SerialName("create_project")
    val createProject: Boolean = false,
    val reconcile: Boolean = false,
    @SerialName("read_results")
    val readResults: Boolean = false,
    val upload: Boolean = false,
)

@Serializable
data class AccountMembership(
    val id: String,
    val name: String,
    val kind: String,
    val role: String,
    val plan: String,
    val capabilities: AccountCapabilities,
)

@Serializable
data class AccountMemberships(
    @SerialName("user_id")
    val userId: String,
    val items: List<AccountMembership>,
)

@Serializable
data class ProjectSettings(
    @SerialName("rules_text")
    val rulesText: String = "",
    @SerialName("use_rules")
    val useRules: Boolean = true,
    @SerialName("rule_ids")
    val ruleIds: List<String> = emptyList(),
    @SerialName("llm_mode")
    val llmMode: String = "",
    @SerialName("web_search")
    val webSearch: Boolean = false,
)

@Serializable
data class SavedProject(
    val id: String,
    val name: String,
    val visibility: String,
    val version: Long,
    val revision: Long,
    val settings: ProjectSettings? = null,
    @SerialName("document_count")
    val documentCount: Int,
    @SerialName("created_at")
    val createdAt: String,
    @SerialName("updated_at")
    val updatedAt: String,
    @SerialName("archived_at")
    val archivedAt: String? = null,
    val capabilities: AccountCapabilities,
    val documents: List<ProjectDocument>? = null,
    @SerialName("shared_with")
    val sharedWith: List<String>? = null,
)

@Serializable
data class ProjectDocument(
    val id: String,
    @SerialName("upload_id")
    val uploadId: String,
    @SerialName("replaces_id")
    val replacesId: String? = null,
    @SerialName("pending_replacement_id")
    val pendingReplacementId: String? = null,
    @SerialName("file_name")
    val fileName: String,
    val role: String,
    val status: String,
    @SerialName("size_bytes")
    val sizeBytes: Long,
    @SerialName("created_at")
    val createdAt: String,
    @SerialName("expires_at")
    val expiresAt: String? = null,
    @SerialName("removed_at")
    val removedAt: String? = null,
)

@Serializable
data class ProjectList(
    val items: List<SavedProject>,
    @SerialName("next_cursor")
    val nextCursor: String? = null,
)

@Serializable
data class ProjectAnalysis(
    val state: String,
    val revision: Long,
    val run: Run? = null,
    val quote: Quote? = null,
)

@Serializable
data class ProjectEvent(
    val id: Long,
    val action: String,
    @SerialName("created_at")
    val createdAt: String,
)

@Serializable
data class ProjectEventList(
    val items: List<ProjectEvent>,
)

@Serializable
data class TeamMember(
    val id: String,
    val name: String,
    val email: String,
    val role: String,
    @SerialName("joined_at")
    val joinedAt: String,
)

@Serializable
data class TeamInvitation(
    val id: String,
    val email: String,
    val role: String,
    val status: String,
    @SerialName("expires_at")
    val expiresAt: String,
    @SerialName("sent_at")
    val sentAt: String,
)

@Serializable
data class TeamInfo(
    val id: String,
    val name: String,
    val kind: String,
    val role: String,
    val capabilities: AccountCapabilities,
    val members: List<TeamMember>,
    val invitations: List<TeamInvitation>,
    @SerialName("member_limit")
    val memberLimit: Int,
    @SerialName("seat_price")
    val seatPrice: Int,
    @SerialName("can_invite")
    val canInvite: Boolean,
)

@Serializable
data class InvitationPreview(
    val id: String,
    val organization: String,
    @SerialName("email_hint")
    val emailHint: String,
    val role: String,
    val status: String,
    @SerialName("expires_at")
    val expiresAt: String,
    @SerialName("email_matches")
    val emailMatches: Boolean,
)

@Serializable
data class InvitationResponse(
    @SerialName("organization_id")
    val organizationId: String,
    val accepted: Boolean,
)

@Serializable data class ProjectRuleProvenance(val origin: String)
@Serializable data class ProjectRule(val id: String,val title: String,val body: String,val version: Int,val status: String,val provenance: ProjectRuleProvenance)
@Serializable data class ProjectRuleList(val items: List<ProjectRule>,@SerialName("next_cursor") val nextCursor: String? = null)

@Serializable
data class InvitationInbox(
    val items: List<InvitationPreview>,
    @SerialName("next_cursor") val nextCursor: String? = null,
)
