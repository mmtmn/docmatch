// HTTP to the docmatch API: JSON in and out, the {code, message, request_id} error envelope as
// ApiException with the server's pt-BR message, one silent refresh on `session_expired`, the end
// of a session revoked on another device, and the User-Agent the API turns into the device name
// (docmatch-android/<version> (<model>; ...)).
package br.com.docmatch.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.withContext
import kotlinx.serialization.KSerializer
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

    suspend fun <T> call(
        method: String,
        path: String,
        body: JsonElement?,
        serializer: KSerializer<T>?,
        idempotent: Boolean = false,
        query: Map<String, String> = emptyMap(),
    ): T? = authed(path) { send(method, path, body, serializer, idempotent, query) }

    /** Raw bytes of a GET (exports). */
    suspend fun bytes(path: String, query: Map<String, String>): ByteArray =
        authed(path) {
            withContext(Dispatchers.IO) {
                val url = url(path).newBuilder().apply { query.forEach { (k, v) -> addQueryParameter(k, v) } }.build()
                execute(api, Request.Builder().url(url).get().build()) { it.body?.bytes() ?: ByteArray(0) }
            }
        }

    /** A presigned URL from the API, with the loopback alias applied when enabled. */
    fun presignedUrl(raw: String): HttpUrl {
        val url = raw.toHttpUrl()
        if (!aliasLoopback || !isLoopback(url.host) || isLoopback(base.host)) return url
        return url.newBuilder().host(base.host).build()
    }

    /**
     * One silent refresh when the access window closed. Any other 401 (or a refresh that is
     * refused) means the session is over, so this device forgets it too.
     */
    private suspend fun <T> authed(path: String, attempt: suspend () -> T): T {
        try {
            return attempt()
        } catch (e: ApiException) {
            if (e.status != 401 || path == REFRESH_PATH) throw e
            if (e.code != SESSION_EXPIRED) throw end(e)
        }
        try {
            send<Unit>("POST", REFRESH_PATH, null, null, false, emptyMap())
            return attempt()
        } catch (e: ApiException) {
            throw if (e.status == 401) end(e) else e
        }
    }

    private fun end(e: ApiException): ApiException {
        if (cookies.hasSession()) {
            cookies.clear()
            ended.tryEmit(e)
        }
        return e
    }

    private suspend fun <T> send(
        method: String,
        path: String,
        body: JsonElement?,
        serializer: KSerializer<T>?,
        idempotent: Boolean,
        query: Map<String, String>,
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
            if (idempotent) builder.header("Idempotency-Key", UUID.randomUUID().toString())
            execute(api, builder.build()) { res ->
                val text = res.body?.string().orEmpty()
                if (serializer == null || res.code == 204 || text.isEmpty()) null else DocmatchJson.decodeFromString(serializer, text)
            }
        }

    internal fun <T> execute(client: OkHttpClient, request: Request, read: (Response) -> T): T {
        val response =
            try {
                client.newCall(request).execute()
            } catch (e: IOException) {
                throw ApiException(0, ApiException.NETWORK, texts.t("errors.network"))
            }
        response.use { res ->
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
            return read(res)
        }
    }

    companion object {
        const val REFRESH_PATH = "/v1/auth/refresh"
        const val SESSION_EXPIRED = "session_expired"

        fun isLoopback(host: String): Boolean = host == "localhost" || host == "::1" || host.startsWith("127.")
    }
}
