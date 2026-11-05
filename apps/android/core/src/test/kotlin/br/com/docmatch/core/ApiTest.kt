package br.com.docmatch.core

import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.After
import org.junit.Before
import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class ApiTest {
    private lateinit var server: MockWebServer

    @Before
    fun start() {
        server = MockWebServer().also { it.start() }
    }

    @After
    fun stop() = server.shutdown()

    private val unauthenticated = """{"code":"unauthenticated","message":"Faça login para continuar."}"""

    private suspend fun signedIn(dm: Docmatch) {
        server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()).addHeader("Set-Cookie", refreshCookie()))
        dm.api.devLogin()
        server.takeRequest()
        assertTrue(dm.cookies.hasSession())
    }

    @Test
    fun `a session revoked on another device is forgotten here and announced`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        val announced = async(start = CoroutineStart.UNDISPATCHED) { dm.sessionEnded.first() }
        server.enqueue(json(401, unauthenticated))
        val e = assertFailsWith<ApiException> { dm.api.runs() }
        assertEquals("unauthenticated", e.code)
        assertFalse(dm.cookies.hasSession(), "os cookies da sessão encerrada saem do aparelho")
        assertEquals("Faça login para continuar.", announced.await().message)
        assertEquals(2, server.requestCount, "sem refresh: a sessão não existe mais")
    }

    @Test
    fun `a refused refresh ends the session too`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        val announced = async(start = CoroutineStart.UNDISPATCHED) { dm.sessionEnded.first() }
        server.enqueue(json(401, """{"code":"session_expired","message":"Sua sessão expirou. Entre de novo."}"""))
        // The API clears both cookies in the refusal itself (session.Manager.Clear).
        server.enqueue(
            json(401, unauthenticated)
                .addHeader("Set-Cookie", "dm_session=; Path=/; Max-Age=0")
                .addHeader("Set-Cookie", "dm_refresh=; Path=/v1/auth/refresh; Max-Age=0"),
        )
        assertFailsWith<ApiException> { dm.api.devices() }
        assertEquals("/v1/me/sessions", server.takeRequest().path)
        assertEquals("/v1/auth/refresh", server.takeRequest().path)
        assertFalse(dm.cookies.hasSession())
        assertEquals("unauthenticated", announced.await().code)
    }

    @Test
    fun `a device that was never signed in announces nothing`() = runTest {
        val dm = newClient(server)
        val announced = async(start = CoroutineStart.UNDISPATCHED) { dm.sessionEnded.first() }
        server.enqueue(json(401, unauthenticated))
        assertNull(dm.api.me())
        assertNull(withTimeoutOrNull(1_000) { announced.await() })
        announced.cancel()
    }

    @Test
    fun `calls in flight when the access window closes refresh once and keep the session`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        val refreshes = java.util.concurrent.atomic.AtomicInteger()
        server.dispatcher =
            object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    val cookie = request.getHeader("Cookie").orEmpty()
                    return when {
                        request.path == "/v1/auth/refresh" -> {
                            refreshes.incrementAndGet()
                            Thread.sleep(150) // the others' 401s arrive while this rotates
                            MockResponse().setResponseCode(204).addHeader("Set-Cookie", sessionCookie("s1.rotated"))
                        }
                        "s1.rotated" in cookie -> json(200, """{"items":[]}""")
                        else -> json(401, """{"code":"session_expired","message":"Sua sessão expirou. Entre de novo."}""")
                    }
                }
            }
        val results = withContext(Dispatchers.IO) { (1..4).map { async { dm.api.devices() } }.awaitAll() }
        assertEquals(List(4) { emptyList<Device>() }, results)
        assertEquals(1, refreshes.get(), "uma renovação só: a segunda com o segredo antigo derrubaria a sessão")
        assertEquals("s1.rotated", dm.cookies.sessionValue())
    }

    @Test
    fun `a 401 for a session another call already rotated retries instead of signing out`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        val events = java.util.concurrent.CopyOnWriteArrayList<ApiException>()
        val watcher = launch(Dispatchers.IO, start = CoroutineStart.UNDISPATCHED) { dm.sessionEnded.collect { events += it } }
        server.dispatcher =
            object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse =
                    when {
                        request.path == "/v1/me/sessions" && "s1.secret" in request.getHeader("Cookie").orEmpty() -> {
                            // Meanwhile another call refreshed: the old access secret is refused.
                            dm.cookies.saveFromResponse(server.url("/"), listOf(okhttp3.Cookie.parse(server.url("/"), sessionCookie("s1.rotated"))!!))
                            json(401, unauthenticated)
                        }
                        else -> json(200, """{"items":[]}""")
                    }
            }
        assertEquals(emptyList(), dm.api.devices())
        assertTrue(dm.cookies.hasSession(), "a sessão nova continua")
        watcher.cancel()
        assertTrue(events.isEmpty(), "nada de sair: outra chamada já renovou")
    }

    @Test
    fun `debug builds reach a loopback upload URL through the API host`() {
        val texts = Texts(realTexts())
        fun transport(base: String, alias: Boolean) = HttpTransport(base, PersistentCookieJar(MemorySecretStore()), "ua", texts, aliasLoopback = alias)
        val local = "http://localhost:8080/v1/uploads/local/upl_1?sig=abc"
        val emulator = transport(server.url("/").toString().replace("localhost", "10.0.2.2").replace("127.0.0.1", "10.0.2.2"), alias = true)
        val aliased = emulator.presignedUrl(local)
        assertEquals("10.0.2.2", aliased.host)
        assertEquals(8080, aliased.port)
        assertEquals("/v1/uploads/local/upl_1?sig=abc", aliased.encodedPath + "?" + aliased.encodedQuery)
        // Release builds and an API on the loopback itself keep the URL as signed.
        assertEquals("localhost", transport(emulator.base.toString(), alias = false).presignedUrl(local).host)
        assertEquals("localhost", transport(server.url("/").toString(), alias = true).presignedUrl(local).host)
        val s3 = "https://uploads.docmatch.invalid/key?X-Amz-Signature=1"
        assertEquals(s3, emulator.presignedUrl(s3).toString())
    }

    @Test
    fun `sign-in stores the device-bound cookies and the next launch reuses them`() = runTest {
        val store = MemorySecretStore()
        server.enqueue(
            json(200, SESSION_JSON)
                .addHeader("Set-Cookie", sessionCookie())
                .addHeader("Set-Cookie", refreshCookie())
                .addHeader("Set-Cookie", deviceCookie()),
        )
        newClient(server, store).api.emailVerify("chl_1", "123456")
        val verify = server.takeRequest()
        assertEquals("/v1/auth/email/verify", verify.path)
        assertEquals("""{"challenge_id":"chl_1","code":"123456"}""", verify.body.readUtf8())
        assertTrue(verify.getHeader("User-Agent")!!.startsWith("docmatch-android/1.0.0 (Pixel 8; Android 15)"))

        // A new process (same encrypted store) presents the same session and device.
        server.enqueue(json(200, """{"user":{"id":"usr_1","email":"ana@example.com"},"org":{"id":"org_1"},"credits":{"balance":10,"reserved":0,"available":10}}"""))
        val me = newClient(server, store).api.me()!!
        assertEquals("ana@example.com", me.user.email)
        assertEquals(10, me.credits!!.available)
        val cookie = server.takeRequest().getHeader("Cookie")!!
        assertTrue("dm_session=s1.secret" in cookie && "dm_device=dev-1" in cookie, cookie)
        assertFalse("dm_refresh" in cookie, "o refresh só vai para /v1/auth/refresh")
    }

    @Test
    fun `an expired access window is refreshed once and the call retried`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        server.enqueue(json(401, """{"code":"session_expired","message":"Sessão expirada."}"""))
        server.enqueue(MockResponse().setResponseCode(204).addHeader("Set-Cookie", sessionCookie("s1.rotated")))
        server.enqueue(json(200, """{"items":[]}"""))
        assertEquals(emptyList(), dm.api.devices())
        assertEquals("/v1/me/sessions", server.takeRequest().path)
        assertEquals("/v1/auth/refresh", server.takeRequest().path)
        assertTrue("s1.rotated" in server.takeRequest().getHeader("Cookie").orEmpty())
    }

    @Test
    fun `errors keep the server code and its pt-BR message`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(402, """{"code":"insufficient_credits","message":"Saldo insuficiente.","request_id":"r1"}"""))
        val e = assertFailsWith<ApiException> { dm.api.estimate(listOf("upl_1"), RunOptions()) }
        assertEquals(402, e.status)
        assertEquals("insufficient_credits", e.code)
        assertEquals("Saldo insuficiente.", e.message)
        assertEquals("r1", e.requestId)

        server.enqueue(MockResponse().setResponseCode(502).setBody("<html>"))
        val bad = assertFailsWith<ApiException> { dm.api.estimate(listOf("upl_1"), RunOptions()) }
        assertEquals("server", bad.code)
        assertTrue(bad.message.contains("502"), bad.message)
    }

    @Test
    fun `a dead network says so in pt-BR`() = runTest {
        val dm = newClient(server)
        server.shutdown()
        val e = assertFailsWith<ApiException> { dm.api.devices() }
        assertEquals(ApiException.NETWORK, e.code)
        assertEquals(dm.texts.t("errors.network"), e.message)
    }

    @Test
    fun `no session means me is null and the methods drive the sign-in screen`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(401, """{"code":"unauthenticated","message":"Faça login para continuar."}"""))
        assertNull(dm.api.me())
        server.enqueue(json(200, """{"email":false,"google":true,"apple":false,"password":true,"dev":false}"""))
        val m = dm.api.methods()
        assertTrue(m.google && !m.email && m.any)
        server.shutdown()
        assertFalse(dm.api.methods().any, "sem API, nenhum botão")
    }

    @Test
    fun `runs are created with an idempotency key and the signed quote`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(202, """{"id":"run_1","status":"queued","sources":[]}"""))
        dm.api.createRun(Quote("quo_1", 40, 50, "sig"), listOf("upl_1", "upl_2"), RunOptions("  tarifa é despesa ", webSearch = true), "key-1")
        val req = server.takeRequest()
        assertEquals("key-1", req.getHeader("Idempotency-Key"))
        val body = req.body.readUtf8()
        assertTrue(""""sources":[{"upload_id":"upl_1"},{"upload_id":"upl_2"}]""" in body, body)
        assertTrue(""""rules_text":"tarifa é despesa"""" in body, body)
        assertTrue(""""quote_id":"quo_1"""" in body && """"quote_signature":"sig"""" in body, body)
        assertTrue(""""options":{"web_search":true,"use_rules":true}""" in body, body)
    }

    @Test
    fun `signing this device out clears its cookies, another device keeps them`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()).addHeader("Set-Cookie", deviceCookie()))
        dm.api.devLogin()
        assertTrue(dm.cookies.hasSession())
        server.takeRequest()
        val other = Device("ses_B", "ios", "App iOS · iPhone", "2026-11-05T08:00:00Z", "2026-11-05T08:30:00Z", current = false)
        server.enqueue(MockResponse().setResponseCode(204))
        dm.api.revokeDevice(other)
        assertEquals("/v1/me/sessions/ses_B", server.takeRequest().path)
        assertTrue(dm.cookies.hasSession(), "encerrar outro aparelho não mexe neste")
        server.enqueue(MockResponse().setResponseCode(204))
        dm.api.revokeDevice(other.copy(id = "ses_A", current = true))
        assertFalse(dm.cookies.hasSession())
    }

    @Test
    fun `deleting the account schedules it and signs this device out`() = runTest {
        val dm = newClient(server)
        signedIn(dm)
        server.enqueue(
            json(202, """{"scheduled_for":"2026-11-12T10:00:00Z"}""")
                .addHeader("Set-Cookie", "dm_session=; Path=/; Max-Age=0")
                .addHeader("Set-Cookie", "dm_refresh=; Path=/v1/auth/refresh; Max-Age=0"),
        )
        assertEquals("2026-11-12T10:00:00Z", dm.api.deleteAccount())
        val req = server.takeRequest()
        assertEquals("DELETE", req.method)
        assertEquals("/v1/me", req.path)
        assertFalse(dm.cookies.hasSession())
    }

    @Test
    fun `logout clears the cookies even when the session was already gone`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()))
        dm.api.devLogin("bia@example.com")
        assertEquals("""{"email":"bia@example.com"}""", server.takeRequest().body.readUtf8())
        server.enqueue(json(401, """{"code":"unauthenticated","message":"x"}"""))
        dm.api.logout()
        assertFalse(dm.cookies.hasSession())
    }
}
