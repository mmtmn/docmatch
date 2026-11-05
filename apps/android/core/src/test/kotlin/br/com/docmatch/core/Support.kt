package br.com.docmatch.core

import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer

/** The real pt-BR texts (packages/locales/pt-BR.json, on the test classpath). */
fun realTexts(): String =
    requireNotNull(Texts::class.java.getResource("/pt-BR.json")) { "pt-BR.json fora do classpath de teste" }.readText()

fun json(status: Int, body: String): MockResponse =
    MockResponse().setResponseCode(status).setHeader("Content-Type", "application/json").setBody(body)

fun sessionCookie(value: String = "s1.secret"): String = "dm_session=$value; Path=/; Max-Age=2592000; HttpOnly"

fun refreshCookie(value: String = "s1.refresh"): String = "dm_refresh=$value; Path=/v1/auth/refresh; Max-Age=2592000; HttpOnly"

fun deviceCookie(value: String = "dev-1"): String = "dm_device=$value; Path=/; Max-Age=34560000; HttpOnly"

fun newClient(server: MockWebServer, store: SecretStore = MemorySecretStore()): Docmatch =
    Docmatch(
        baseUrl = server.url("/").toString(),
        store = store,
        userAgent = Docmatch.androidUserAgent("1.0.0", "Pixel 8", "15"),
        textsJson = realTexts(),
    )

const val SESSION_JSON = """{"user":{"id":"usr_1","email":"ana@example.com","display_name":"Ana"},"org":{"id":"org_1","name":"Ana"},"expires_at":"2026-11-05T12:00:00Z"}"""
