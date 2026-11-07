// HTTP to the docmatch API: JSON in and out, the {code, message, request_id} error envelope as
// ApiException with the server's pt-BR message, one refresh at a time when the access window
// closes, the end of a session revoked on another device, and the User-Agent the API turns into
// the device name (docmatch-android/<version> (<model>; ...)).
package br.com.docmatch.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.JsonElement
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.UUID
import java.util.concurrent.TimeUnit

/** An API error with the server's code and pt-BR message (or a local one for network failures). */
class ApiException(
    val status: Int,
    val code: String,
    override val message: String,
    val requestId: String? = null,
) : Exception(message) {
    companion object {
        const val NETWORK = "network"
        const val UPLOAD_REJECTED = "upload_rejected"
        const val UPLOAD_FAILED = "upload_failed"
    }
}

private val JSON = "application/json".toMediaType()

/**
 * Builds requests against one API origin with a shared cookie jar. [aliasLoopback] is for debug
 * builds only: the local API signs upload URLs for its own loopback, which an emulator (10.0.2.2)
 * or a phone on the LAN reaches through the API's host instead.
 */
class HttpTransport(
    baseUrl: String,
    val cookies: PersistentCookieJar,
    private val userAgent: String,
    private val texts: Texts,
    client: OkHttpClient = OkHttpClient(),
    private val aliasLoopback: Boolean = false,
) {
    val base: HttpUrl = baseUrl.trimEnd('/').toHttpUrl()

    private val ended = MutableSharedFlow<ApiException>(extraBufferCapacity = 1, onBufferOverflow = BufferOverflow.DROP_OLDEST)
    private val refreshing = Mutex()
    private var lastEnded: String? = null

    /**
     * Emits when the API stops accepting this device's session: revoked from another device's
     * account page, signed out everywhere, or past its lifetime. The cookies are already gone.
     */
    val sessionEnded: SharedFlow<ApiException> = ended

    /** Requests to the API: cookies, JSON, User-Agent. */
    val api: OkHttpClient =
        client.newBuilder()
            .cookieJar(cookies)
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(60, TimeUnit.SECONDS)
            .addInterceptor { chain ->
                chain.proceed(chain.request().newBuilder().header("User-Agent", userAgent).build())
            }
            .build()

    /** Requests to presigned URLs: never any cookie (the signature is the credential). */
    val presigned: OkHttpClient =
        client.newBuilder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .writeTimeout(5, TimeUnit.MINUTES)
            .readTimeout(60, TimeUnit.SECONDS)
            .addInterceptor { chain ->
                chain.proceed(chain.request().newBuilder().header("User-Agent", userAgent).build())
            }
            .build()

    fun url(path: String): HttpUrl =
        base.newBuilder().encodedPath(if (path.startsWith("/")) path else "/$path").build()

    /**
     * One API call. [idempotencyKey] makes a retry of the same operation safe (the run created
     * from one accepted estimate); [idempotent] asks for a fresh key, kept across the refresh retry.
     */
    suspend fun <T> call(
        method: String,
        path: String,
        body: JsonElement?,
        serializer: KSerializer<T>?,
        idempotent: Boolean = false,
        query: Map<String, String> = emptyMap(),
        idempotencyKey: String? = null,
        organizationId: String? = null,
    ): T? {
        val key = idempotencyKey ?: if (idempotent) UUID.randomUUID().toString() else null
        return authed(path) { send(method, path, body, serializer, key, query, organizationId) }
    }

    /** Raw bytes of a GET (exports). */
    suspend fun bytes(path: String, query: Map<String, String>, organizationId: String? = null): ByteArray =
        authed(path) {
            withContext(Dispatchers.IO) {
                val url = url(path).newBuilder().apply { query.forEach { (k, v) -> addQueryParameter(k, v) } }.build()
                execute(api, Request.Builder().url(url).get().apply { if (organizationId != null) header("X-Docmatch-Org", organizationId) }.build()) { it.body?.bytes() ?: ByteArray(0) }
            }
        }

    /** A presigned URL from the API, with the loopback alias applied when enabled. */
    fun presignedUrl(raw: String): HttpUrl {
        val url = raw.toHttpUrl()
        if (!aliasLoopback || !isLoopback(url.host) || isLoopback(base.host)) return url
        return url.newBuilder().host(base.host).build()
    }

    /**
     * The session rules of every call. A 401 for a session that another call already refreshed
     * just retries; `session_expired` refreshes, one call at a time (a second refresh with the
     * rotated-out secret would make the server revoke the whole session); any other 401 for the
     * session this request carried means it is over, and this device forgets it too.
     */
    private suspend fun <T> authed(path: String, attempt: suspend () -> T): T {
        val sent = cookies.sessionValue()
        try {
            return attempt()
        } catch (e: ApiException) {
            if (e.status != 401 || path == REFRESH_PATH || sent == null) throw e
            refreshing.withLock {
                val current = cookies.sessionValue()
                when {
                    current == null -> throw e // ended by another call, which announced it
                    current != sent -> Unit // refreshed by another call meanwhile: just retry
                    e.code != SESSION_EXPIRED -> throw end(e, sent)
                    else ->
                        try {
                            send<Unit>("POST", REFRESH_PATH, null, null, null, emptyMap())
                        } catch (r: ApiException) {
                            // A refused refresh clears the cookies in its own answer; the session
                            // this call carried is what ends.
                            throw if (r.status == 401) end(r, sent) else r
                        }
                }
            }
        }
        val retried = cookies.sessionValue()
        try {
            return attempt()
        } catch (e: ApiException) {
            if (e.status == 401 && retried != null && cookies.sessionValue() == retried) throw end(e, retried)
            throw e
        }
    }

    /** Forgets the session (once per session value) and announces it. */
    private fun end(e: ApiException, session: String): ApiException {
        synchronized(this) {
            if (lastEnded == session) return e
            lastEnded = session
        }
        cookies.clear()
        ended.tryEmit(e)
        return e
    }

    private suspend fun <T> send(
        method: String,
        path: String,
        body: JsonElement?,
        serializer: KSerializer<T>?,
        idempotencyKey: String?,
        query: Map<String, String>,
        organizationId: String? = null,
    ): T? =
        withContext(Dispatchers.IO) {
            val url = url(path).newBuilder().apply { query.forEach { (k, v) -> addQueryParameter(k, v) } }.build()
            val requestBody: RequestBody? =
                when {
                    body != null -> DocmatchJson.encodeToString(JsonElement.serializer(), body).toRequestBody(JSON)
                    method == "POST" || method == "PUT" || method == "PATCH" -> ByteArray(0).toRequestBody(null)
                    else -> null
                }
            val builder = Request.Builder().url(url).method(method, requestBody).header("Accept", "application/json")
            if (organizationId != null) builder.header("X-Docmatch-Org", organizationId)
            if (idempotencyKey != null) builder.header("Idempotency-Key", idempotencyKey)
            execute(api, builder.build()) { res ->
                val text = res.body?.string().orEmpty()
                if (serializer == null || res.code == 204 || text.isEmpty()) null else DocmatchJson.decodeFromString(serializer, text)
            }
        }

    /**
     * Runs a request and reads its answer. Network failures (also halfway through the body) become
     * the pt-BR network error, and an answer that does not decode the generic one: nothing below
     * the screens throws anything but ApiException.
     */
    internal fun <T> execute(client: OkHttpClient, request: Request, read: (Response) -> T): T {
        try {
            client.newCall(request).execute().use { res ->
                if (!res.isSuccessful) {
                    val text = runCatching { res.body?.string() }.getOrNull().orEmpty()
                    val env = runCatching { DocmatchJson.decodeFromString(ErrorEnvelope.serializer(), text) }.getOrNull()
                    throw ApiException(
                        res.code,
                        env?.code?.ifEmpty { null } ?: "server",
                        env?.message?.ifEmpty { null } ?: texts.t("errors.server", mapOf("status" to res.code)),
                        env?.requestId,
                    )
                }
                return try {
                    read(res)
                } catch (e: SerializationException) {
                    throw ApiException(res.code, "server", texts.t("errors.unknown"))
                } catch (e: IllegalArgumentException) {
                    throw ApiException(res.code, "server", texts.t("errors.unknown"))
                }
            }
        } catch (e: IOException) {
            throw ApiException(0, ApiException.NETWORK, texts.t("errors.network"))
        }
    }

    companion object {
        const val REFRESH_PATH = "/v1/auth/refresh"
        const val SESSION_EXPIRED = "session_expired"

        fun isLoopback(host: String): Boolean = host == "localhost" || host == "::1" || host.startsWith("127.")
    }
}
