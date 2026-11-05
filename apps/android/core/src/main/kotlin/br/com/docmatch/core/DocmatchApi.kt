// Every API call the app makes, one method per route of packages/contracts/openapi.yaml: the same
// requests the site and the iOS app send.
package br.com.docmatch.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import okhttp3.MediaType
import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.MultipartBody
import okhttp3.Request
import okhttp3.RequestBody
import okio.BufferedSink
import okio.source
import java.io.InputStream
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

    /**
     * Asks for the account's deletion (LGPD): everything goes on the returned date, every device
     * is signed out now, and this one forgets its cookies.
     */
    suspend fun deleteAccount(): String {
        val scheduled = http.call("DELETE", "/v1/me", null, DeletionScheduled.serializer())!!
        http.cookies.clear()
        return scheduled.scheduledFor
    }

    /** Signs one device out; signing out this one also clears the local cookies. */
    suspend fun revokeDevice(device: Device) {
        http.call<Unit>("DELETE", "/v1/me/sessions/${enc(device.id)}", null, null)
        if (device.current) http.cookies.clear()
    }

    // ------------------------------------------------------------------ uploads

    /** A document (with its SHA-256) or media (audio, video: a PUT, no hash) upload ticket. */
    suspend fun createUpload(file: LocalFile, sha256: String?): UploadTicket =
        http.call(
            "POST",
            "/v1/uploads",
            buildJsonObject {
                put("kind", if (file.kind.media) "media" else "document")
                put("file_name", file.name)
                put("size_bytes", file.size)
                put("mime", file.mime)
                if (sha256 != null) put("sha256", sha256)
            },
            UploadTicket.serializer(),
        )!!

    /** Streams the file to the presigned ticket (S3 in production, the API's local folder in dev). */
    suspend fun putObject(ticket: UploadTicket, file: LocalFile) {
        withContext(Dispatchers.IO) {
            val body = StreamBody(file.mime.toMediaTypeOrNull(), file.size, file.open)
            val request =
                if (ticket.method.equals("POST", ignoreCase = true)) {
                    val form = MultipartBody.Builder().setType(MultipartBody.FORM)
                    ticket.fields.forEach { (k, v) -> form.addFormDataPart(k, v) }
                    form.addFormDataPart("file", file.name, body) // last: S3 ignores later fields
                    Request.Builder().url(http.presignedUrl(ticket.url)).post(form.build()).build()
                } else {
                    val b = Request.Builder().url(http.presignedUrl(ticket.url)).put(body)
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

    suspend fun estimate(uploadIds: List<String>, options: RunOptions): Quote =
        http.call("POST", "/v1/runs/estimate", runBody(uploadIds, options), Quote.serializer())!!

    /** Starts the run of an accepted estimate; the same key on a retry never starts a second run. */
    suspend fun createRun(quote: Quote, uploadIds: List<String>, options: RunOptions, idempotencyKey: String): Run {
        val body =
            JsonObject(
                runBody(uploadIds, options) +
                    buildJsonObject {
                        put("quote_id", quote.id)
                        put("quote_signature", quote.signature)
                    },
            )
        return http.call("POST", "/v1/runs", body, Run.serializer(), idempotencyKey = idempotencyKey)!!
    }

    /** Approves the extra estimate of a run parked at needs_approval; it resumes from its checkpoint. */
    suspend fun approveQuote(runId: String, quote: Quote, idempotencyKey: String): Run =
        http.call(
            "POST",
            "/v1/runs/${enc(runId)}/approve-quote",
            buildJsonObject {
                put("quote_id", quote.id)
                put("quote_signature", quote.signature)
            },
            Run.serializer(),
            idempotencyKey = idempotencyKey,
        )!!

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
            cursor = page.nextCursor ?: return out.distinctBy { it.id }
        }
        return out.distinctBy { it.id }
    }

    /** Records a decision (confirmed, dismissed, resolved with a note) the site and the other apps see. */
    suspend fun feedback(runId: String, findingId: String, decision: String, note: String? = null) {
        http.call<Unit>(
            "POST",
            "/v1/runs/${enc(runId)}/findings/${enc(findingId)}/feedback",
            buildJsonObject {
                put("status", decision)
                if (!note.isNullOrBlank()) put("note", note.trim())
            },
            null,
            idempotent = true,
        )
    }

    /** The run's report as XLSX or CSV bytes (the formats the API exports). */
    suspend fun export(runId: String, format: String): ByteArray =
        http.bytes("/v1/runs/${enc(runId)}/export", mapOf("format" to format))

    private fun runBody(uploadIds: List<String>, options: RunOptions): JsonObject =
        buildJsonObject {
            putJsonArray("sources") { uploadIds.forEach { add(buildJsonObject { put("upload_id", it) }) } }
            if (options.rulesText.isNotBlank()) put("rules_text", options.rulesText.trim())
            putJsonObject("options") {
                put("web_search", options.webSearch)
                put("use_rules", options.useRules)
            }
        }

    private fun enc(segment: String): String = URLEncoder.encode(segment, Charsets.UTF_8).replace("+", "%20")

    private companion object {
        const val MAX_PAGES = 50
    }
}

/**
 * A file picked on the phone. Only its name, size and kind are kept; the bytes are streamed from
 * [open] while hashing and uploading, so a large file never sits in memory.
 */
class LocalFile(val name: String, val mime: String, val size: Long, val kind: FileKind, val open: () -> InputStream) {
    companion object {
        /** A file from bytes already in memory (tests, pasted text), classified like a picked one. */
        fun of(name: String, bytes: ByteArray): LocalFile {
            val (kind, refusal) = Precheck.classify(name, bytes.copyOf(minOf(bytes.size, Precheck.HEAD_BYTES)))
            require(kind != null) { "arquivo recusado: ${refusal?.key}" }
            val mime = requireNotNull(Precheck.mime(kind, name)) { "tipo sem MIME: $kind" }
            return LocalFile(name, mime, bytes.size.toLong(), kind) { bytes.inputStream() }
        }
    }
}

/** A request body streamed from the file, reopened on each attempt (OkHttp may retry). */
private class StreamBody(private val type: MediaType?, private val size: Long, private val open: () -> InputStream) : RequestBody() {
    override fun contentType(): MediaType? = type

    override fun contentLength(): Long = size

    override fun writeTo(sink: BufferedSink) {
        open().source().use { sink.writeAll(it) }
    }
}
