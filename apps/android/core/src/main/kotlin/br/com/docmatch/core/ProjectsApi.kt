package br.com.docmatch.core

import java.net.URLEncoder
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray

private fun segment(s: String) = URLEncoder.encode(s, Charsets.UTF_8).replace("+", "%20")

private fun projectPath(id: String) = "/v1/projects/${segment(id)}"

/** Identity lookup works even after this account's membership was revoked. */
suspend fun DocmatchApi.organizations(): AccountMemberships =
    transport.call("GET", "/v1/me/organizations", null, AccountMemberships.serializer())!!

suspend fun DocmatchApi.projects(cursor: String? = null, archived: Boolean = false): ProjectList =
    http.call(
        "GET",
        "/v1/projects",
        null,
        ProjectList.serializer(),
        query =
            buildMap {
                put("archived", archived.toString())
                if (cursor != null) put("cursor", cursor)
            },
    )!!

suspend fun DocmatchApi.createProject(name: String): SavedProject =
    http.call(
        "POST",
        "/v1/projects",
        buildJsonObject { put("name", name) },
        SavedProject.serializer(),
    )!!

suspend fun DocmatchApi.project(id: String): SavedProject =
    http.call("GET", projectPath(id), null, SavedProject.serializer())!!

suspend fun DocmatchApi.updateProject(
    id: String,
    version: Long,
    name: String? = null,
    archived: Boolean? = null,
    settings: ProjectSettings? = null,
): SavedProject {
    val body = buildJsonObject {
        put("version", version)
        if (name != null) put("name", name)
        if (archived != null) put("archived", archived)
        if (settings != null)
            put(
                "settings",
                buildJsonObject {
                    put("rules_text", settings.rulesText)
                    put("use_rules", settings.useRules)
                    put("llm_mode", settings.llmMode)
                    put("web_search", settings.webSearch)
                    putJsonArray("rule_ids") { settings.ruleIds.forEach { add(JsonPrimitive(it)) } }
                },
            )
    }
    return http.call("PATCH", projectPath(id), body, SavedProject.serializer())!!
}

suspend fun DocmatchApi.deleteProject(id: String, version: Long) {
    http.call<Unit>(
        "DELETE",
        projectPath(id),
        null,
        null,
        query = mapOf("version" to version.toString()),
    )
}

suspend fun DocmatchApi.shareProject(
    id: String,
    version: Long,
    visibility: String,
    users: List<String>,
): SavedProject =
    http.call(
        "PUT",
        projectPath(id) + "/sharing",
        buildJsonObject {
            put("version", version)
            put("visibility", visibility)
            putJsonArray("user_ids") { users.forEach { add(JsonPrimitive(it)) } }
        },
        SavedProject.serializer(),
    )!!

suspend fun DocmatchApi.removeProjectDocument(id: String, document: String, version: Long) {
    http.call<Unit>(
        "DELETE",
        projectPath(id) + "/documents/" + segment(document),
        null,
        null,
        query = mapOf("version" to version.toString()),
    )
}

suspend fun DocmatchApi.replaceProjectDocument(
    id: String,
    document: String,
    replacement: String,
    version: Long,
): SavedProject =
    http.call(
        "POST",
        projectPath(id) + "/documents/" + segment(document) + "/replace",
        buildJsonObject {
            put("version", version)
            put("replacement_id", replacement)
        },
        SavedProject.serializer(),
    )!!

suspend fun DocmatchApi.projectActivity(id: String): ProjectEventList =
    http.call("GET", projectPath(id) + "/activity", null, ProjectEventList.serializer())!!

suspend fun DocmatchApi.activity(cursor: String? = null): RunList =
    http.call(
        "GET",
        "/v1/projects/activity",
        null,
        RunList.serializer(),
        query = if (cursor != null) mapOf("cursor" to cursor) else emptyMap(),
    )!!

suspend fun DocmatchApi.estimateProject(
    id: String,
    revision: Long,
    reprocessReason: String = "",
): ProjectAnalysis =
    http.call(
        "POST",
        projectPath(id) + "/runs/estimate",
        buildJsonObject {
            put("revision", revision)
            put("reprocess_reason", reprocessReason)
        },
        ProjectAnalysis.serializer(),
    )!!

/** Only call after the user explicitly approves the quote cap; unchanged work returns its run. */
suspend fun DocmatchApi.reconcileProject(
    id: String,
    revision: Long,
    quote: Quote?,
    idempotencyKey: String,
    reprocessReason: String = "",
): ProjectAnalysis =
    http.call(
        "POST",
        projectPath(id) + "/runs",
        buildJsonObject {
            put("revision", revision)
            put("reprocess_reason", reprocessReason)
            if (quote != null) {
                put("quote_id", quote.id)
                put("quote_signature", quote.signature)
            }
        },
        ProjectAnalysis.serializer(),
        idempotencyKey = idempotencyKey,
    )!!

suspend fun DocmatchApi.team(): TeamInfo =
    http.call("GET", "/v1/team", null, TeamInfo.serializer())!!

suspend fun DocmatchApi.convertToTeam(name: String): TeamInfo =
    http.call("POST", "/v1/team", buildJsonObject { put("name", name) }, TeamInfo.serializer())!!

suspend fun DocmatchApi.invite(email: String, role: String): TeamInvitation =
    http.call(
        "POST",
        "/v1/team/invitations",
        buildJsonObject {
            put("email", email)
            put("role", role)
        },
        TeamInvitation.serializer(),
    )!!

suspend fun DocmatchApi.resendInvitation(id: String) {
    http.call<Unit>("POST", "/v1/team/invitations/${segment(id)}/resend", null, null)
}

suspend fun DocmatchApi.revokeInvitation(id: String) {
    http.call<Unit>("DELETE", "/v1/team/invitations/${segment(id)}", null, null)
}

suspend fun DocmatchApi.changeMemberRole(id: String, role: String) {
    http.call<Unit>(
        "PATCH",
        "/v1/team/members/${segment(id)}",
        buildJsonObject { put("role", role) },
        null,
    )
}

suspend fun DocmatchApi.removeMember(id: String) {
    http.call<Unit>("DELETE", "/v1/team/members/${segment(id)}", null, null)
}

suspend fun DocmatchApi.transferOwnership(id: String) {
    http.call<Unit>("POST", "/v1/team/owner", buildJsonObject { put("user_id", id) }, null)
}

suspend fun DocmatchApi.leaveTeam() {
    http.call<Unit>("POST", "/v1/team/leave", null, null)
}

suspend fun DocmatchApi.invitation(token: String): InvitationPreview =
    transport.call(
        "POST",
        "/v1/invitations/preview",
        buildJsonObject { put("token", token) },
        InvitationPreview.serializer(),
    )!!

suspend fun DocmatchApi.respondToInvitation(token: String, accept: Boolean): InvitationResponse =
    transport.call(
        "POST",
        "/v1/invitations/respond",
        buildJsonObject {
            put("token", token)
            put("accept", accept)
        },
        InvitationResponse.serializer(),
    )!!

suspend fun DocmatchApi.projectRules(id: String, cursor: String? = null): ProjectRuleList =
    http.call(
        "GET",
        projectPath(id) + "/rules",
        null,
        ProjectRuleList.serializer(),
        query = if (cursor != null) mapOf("cursor" to cursor) else emptyMap(),
    )!!

suspend fun DocmatchApi.saveProjectRule(
    id: String,
    rule: String? = null,
    title: String,
    body: String,
    idempotencyKey: String,
): ProjectRule =
    http.call(
        "POST",
        projectPath(id) + "/rules" + (rule?.let { "/${segment(it)}/versions" } ?: ""),
        buildJsonObject {
            put("title", title)
            put("body", body)
        },
        ProjectRule.serializer(),
        idempotencyKey = idempotencyKey,
    )!!

suspend fun DocmatchApi.promoteProjectRule(
    id: String,
    rule: String,
    to: String,
    idempotencyKey: String,
): ProjectRule =
    http.call(
        "POST",
        projectPath(id) + "/rules/${segment(rule)}/promote",
        buildJsonObject { put("to", to) },
        ProjectRule.serializer(),
        idempotencyKey = idempotencyKey,
    )!!

suspend fun DocmatchApi.cancelRun(id: String): Run =
    http.call(
        "POST",
        "/v1/runs/${segment(id)}/cancel",
        null,
        Run.serializer(),
        idempotencyKey = java.util.UUID.randomUUID().toString(),
    )!!
