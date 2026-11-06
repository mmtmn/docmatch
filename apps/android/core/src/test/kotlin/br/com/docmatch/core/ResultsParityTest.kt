package br.com.docmatch.core

import kotlinx.serialization.json.Json
import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockWebServer
import org.junit.Test
import kotlin.test.*

class ResultsParityTest {
    @Test fun `snapshot retains confirmed and proposed balances separately and old runs remain readable`() {
        val json = Json { ignoreUnknownKeys = true }
        val body = requireNotNull(javaClass.getResource("/reconciliation.json")).readText()
        val run = json.decodeFromString<Run>(body)
        val doc = requireNotNull(run.summary?.reconciliation)
        assertEquals(6000L, doc.totals.openBalance)
        assertEquals(4000L, doc.totals.projectedOpenBalance)
        assertEquals(2000L, doc.receivables.single().proposedAmount)
        assertNull(json.decodeFromString<Run>("""{"id":"old","status":"completed","summary":{}}""").summary?.reconciliation)
        assertTrue(findingPriority("critical") < findingPriority("high"))
        assertTrue(findingPriority("low") < findingPriority("info"))
    }
    @Test fun `password sign in can repeat after sign out and does not send an unused code`() = runTest {
        MockWebServer().use { server ->
            server.start()
            val dm = newClient(server)
            repeat(2) {
                server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()))
                dm.api.passwordLogin("ana@example.com", "test-only-password")
                val req = server.takeRequest()
                assertEquals("/v1/auth/password/login", req.path)
                assertFalse(req.body.readUtf8().contains("totp"))
                assertTrue(dm.cookies.hasSession())
                server.enqueue(json(204, "")); dm.api.logout(); server.takeRequest()
                assertFalse(dm.cookies.hasSession())
            }
        }
    }
}
