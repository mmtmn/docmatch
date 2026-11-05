// Every API call the app makes, one method per route of packages/contracts/openapi.yaml.
package br.com.docmatch.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.MultipartBody
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.net.URLEncoder

class DocmatchApi(private val http: HttpTransport) {
    // ------------------------------------------------------------------ account and sign-in

    /** The sign-ins this deployment offers; none when the API cannot be reached. */
    suspend fun methods(): AuthMethods =
        runCatching { http.call("GET", "/v1/auth/methods", null, AuthMethods.serializer()) }.getOrNull() ?: AuthMethods()

    /** The signed-in account, or null when this device has no valid session. */
    suspend fun me(): Me? =
        try {
            http.call("GET", "/v1/me", null, Me.serializer())
        } catch (e: ApiException) {
            if (e.status == 401) null else throw e
        }

    suspend fun emailStart(email: String): EmailChallenge =
        http.call("POST", "/v1/auth/email/start", buildJsonObject { put("email", email) }, EmailChallenge.serializer())!!

    suspend fun emailVerify(challengeId: String, code: String): SessionInfo =
        http.call(
            "POST",
            "/v1/auth/email/verify",
            buildJsonObject {
                put("challenge_id", challengeId)
                put("code", code)
            },
            SessionInfo.serializer(),
        )!!

    /** The ID token from Google's own sign-in on the phone (Credential Manager). */
    suspend fun googleNative(idToken: String): SessionInfo =
        http.call("POST", "/v1/auth/google/native", buildJsonObject { put("id_token", idToken) }, SessionInfo.serializer())!!

    /** Local sign-in (development API only). */
    suspend fun devLogin(email: String? = null): SessionInfo =
        http.call(
            "POST",
            "/v1/auth/dev/login",
            buildJsonObject { if (email != null) put("email", email) },
            SessionInfo.serializer(),
        )!!

    /** Signs this device out (the server revokes the session; the cookies go too). */
    suspend fun logout() {
        try {
            http.call<Unit>("POST", "/v1/auth/logout", null, null)
        } catch (e: ApiException) {
            if (e.status != 401) throw e
        } finally {
            http.cookies.clear()
        }
    }

    suspend fun devices(): List<Device> = http.call("GET", "/v1/me/sessions", null, DeviceList.serializer())!!.items

    /** Signs one device out; signing out this one also clears the local cookies. */
    suspend fun revokeDevice(device: Device) {
        http.call<Unit>("DELETE", "/v1/me/sessions/${enc(device.id)}", null, null)
        if (device.current) http.cookies.clear()
    }

    // ------------------------------------------------------------------ uploads

    suspend fun createUpload(file: LocalFile, sha256: String): UploadTicket =
        http.call(
            "POST",
            "/v1/uploads",
            buildJsonObject {
                put("kind", "document")
                put("file_name", file.name)
                put("size_bytes", file.bytes.size)
                put("mime", file.mime)
                put("sha256", sha256)
            },
            UploadTicket.serializer(),
        )!!

    /** Sends the bytes to the presigned ticket (S3 in production, the API's local folder in dev). */
    suspend fun putObject(ticket: UploadTicket, file: LocalFile) {
        withContext(Dispatchers.IO) {
            val type = file.mime.toMediaTypeOrNull()
            val request =
                if (ticket.method.equals("POST", ignoreCase = true)) {
                    val form = MultipartBody.Builder().setType(MultipartBody.FORM)
                    ticket.fields.forEach { (k, v) -> form.addFormDataPart(k, v) }
                    form.addFormDataPart("file", file.name, file.bytes.toRequestBody(type)) // last: S3 ignores later fields
                    Request.Builder().url(http.presignedUrl(ticket.url)).post(form.build()).build()
                } else {
                    val b = Request.Builder().url(http.presignedUrl(ticket.url)).put(file.bytes.toRequestBody(type))
                    ticket.headers.forEach { (k, v) -> b.header(k, v) }
                    b.build()
                }
            http.execute(http.presigned, request) { }
        }
    }

    suspend fun completeUpload(id: String, sha256: String?): Upload =
        http.call(
            "POST",
            "/v1/uploads/${enc(id)}/complete",
            buildJsonObject { if (sha256 != null) put("sha256", sha256) },
            Upload.serializer(),
        )!!

    // ------------------------------------------------------------------ runs

    suspend fun estimate(uploadIds: List<String>, rulesText: String): Quote =
        http.call("POST", "/v1/runs/estimate", runBody(uploadIds, rulesText), Quote.serializer())!!

    suspend fun createRun(quote: Quote, uploadIds: List<String>, rulesText: String): Run {
        val body =
            JsonObject(
                runBody(uploadIds, rulesText) +
                    buildJsonObject {
                        put("quote_id", quote.id)
                        put("quote_signature", quote.signature)
                    },
            )
        return http.call("POST", "/v1/runs", body, Run.serializer(), idempotent = true)!!
    }

    suspend fun run(id: String): Run = http.call("GET", "/v1/runs/${enc(id)}", null, Run.serializer())!!

    suspend fun runs(cursor: String? = null, limit: Int = 30): RunList =
        http.call(
            "GET",
            "/v1/runs",
            null,
            RunList.serializer(),
            query = buildMap {
                put("limit", limit.toString())
                if (cursor != null) put("cursor", cursor)
            },
        )!!

    /** Every finding of a run, page after page. */
    suspend fun findings(runId: String): List<Finding> {
        val out = mutableListOf<Finding>()
        var cursor: String? = null
        repeat(MAX_PAGES) {
            val page =
                http.call(
                    "GET",
                    "/v1/runs/${enc(runId)}/findings",
                    null,
                    FindingList.serializer(),
                    query = buildMap {
                        put("limit", "100")
                        if (cursor != null) put("cursor", cursor!!)
                    },
                )!!
            out += page.items
            cursor = page.nextCursor ?: return out
        }
        return out
    }

    /** Records a decision (confirmed, dismissed, resolved) the web and the other apps then see. */
    suspend fun feedback(runId: String, findingId: String, decision: String, note: String? = null) {
        http.call<Unit>(
            "POST",
            "/v1/runs/${enc(runId)}/findings/${enc(findingId)}/feedback",
            buildJsonObject {
                put("status", decision)
                if (!note.isNullOrBlank()) put("note", note)
            },
            null,
            idempotent = true,
        )
    }

    /** The run's report as XLSX or PDF bytes. */
    suspend fun export(runId: String, format: String): ByteArray =
        http.bytes("/v1/runs/${enc(runId)}/export", mapOf("format" to format))

    private fun runBody(uploadIds: List<String>, rulesText: String): JsonObject =
        buildJsonObject {
            putJsonArray("sources") { uploadIds.forEach { add(buildJsonObject { put("upload_id", it) }) } }
            if (rulesText.isNotBlank()) put("rules_text", rulesText.trim())
            putJsonObject("options") { put("use_rules", true) }
        }

    private fun enc(segment: String): String = URLEncoder.encode(segment, Charsets.UTF_8).replace("+", "%20")

    private companion object {
        const val MAX_PAGES = 50
    }
}

/** A file picked on the phone, read into memory (documents are capped at tens of MB). */
class LocalFile(val name: String, val mime: String, val bytes: ByteArray)
