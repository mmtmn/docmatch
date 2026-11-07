// Each client keeps an immutable account and project; refresh shares one transport lock.
package br.com.docmatch.core

import kotlinx.serialization.KSerializer
import kotlinx.serialization.json.JsonElement
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response

internal class AccountHttp(val raw: HttpTransport, val organizationId: String?, val projectId: String?) {
    val cookies get() = raw.cookies
    val presigned get() = raw.presigned
    fun presignedUrl(value: String) = raw.presignedUrl(value)
    fun <T> execute(client: OkHttpClient, request: Request, read: (Response) -> T) = raw.execute(client, request, read)
    private fun path(value: String): String {
        if (projectId == null) return value
        require(Regex("prj_[A-Za-z0-9]+").matches(projectId))
        return if (value == "/v1/runs" || value.startsWith("/v1/runs/") || value == "/v1/uploads" || value.startsWith("/v1/uploads/"))
            "/v1/projects/$projectId" + value.removePrefix("/v1") else value
    }
    suspend fun <T> call(method: String, path: String, body: JsonElement?, serializer: KSerializer<T>?, idempotent: Boolean = false, query: Map<String, String> = emptyMap(), idempotencyKey: String? = null): T? =
        raw.call(method, path(path), body, serializer, idempotent, query, idempotencyKey, organizationId)
    suspend fun bytes(path: String, query: Map<String, String>) = raw.bytes(path(path), query, organizationId)
}
