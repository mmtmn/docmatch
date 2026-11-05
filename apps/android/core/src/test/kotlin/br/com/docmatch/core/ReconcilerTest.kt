package br.com.docmatch.core

import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Before
import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull
import kotlin.test.assertTrue

class ReconcilerTest {
    private lateinit var server: MockWebServer

    @Before
    fun start() {
        server = MockWebServer().also { it.start() }
    }

    @After
    fun stop() = server.shutdown()

    private fun reconciler(dm: Docmatch) = Reconciler(dm.api, dm.texts, pollMillis = 1, sleep = {})

    @Test
    fun `uploads through the presigned form without cookies, waits for the scan, estimates and runs`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()))
        dm.api.devLogin()
        server.takeRequest()

        val presigned = server.url("/presigned/bucket").toString()
        server.enqueue(json(201, """{"id":"upl_1","method":"POST","url":"$presigned","fields":{"key":"k","policy":"p"}}"""))
        server.enqueue(MockResponse().setResponseCode(204))
        server.enqueue(json(202, """{"id":"upl_1","file_name":"extrato.csv","status":"scanning"}"""))
        server.enqueue(json(200, """{"id":"upl_1","file_name":"extrato.csv","status":"ready"}"""))
        server.enqueue(json(200, """{"id":"quo_1","expected_credits":40,"cap_credits":50,"signature":"sig","requires_approval":false}"""))

        val steps = mutableListOf<Step>()
        val bytes = "data;valor\n16/07/2026;1.000,00\n".encodeToByteArray()
        val prepared = reconciler(dm).prepare(listOf(LocalFile.of("extrato.csv", bytes)), RunOptions(), steps::add)
        assertEquals(40, prepared.quote.expectedCredits)
        assertEquals(listOf("upl_1"), prepared.uploadIds)
        assertEquals(listOf(Step.Uploading(1, 1), Step.Scanning("extrato.csv"), Step.Estimating), steps)

        val create = server.takeRequest()
        assertEquals("/v1/uploads", create.path)
        val createBody = create.body.readUtf8()
        assertTrue(""""sha256":"${Reconciler.sha256Hex(bytes)}"""" in createBody, createBody)
        assertTrue(""""size_bytes":${bytes.size}""" in createBody && """"kind":"document"""" in createBody, createBody)

        val upload = server.takeRequest()
        assertEquals("/presigned/bucket", upload.path)
        assertNull(upload.getHeader("Cookie"), "URL pré-assinada nunca leva cookie")
        val form = upload.body.readUtf8()
        assertTrue(form.indexOf("name=\"key\"") < form.indexOf("name=\"file\""), "o arquivo vai por último")
        assertTrue("data;valor" in form)

        assertEquals("""{"sha256":"${Reconciler.sha256Hex(bytes)}"}""", server.takeRequest().body.readUtf8())
        assertEquals("{}", server.takeRequest().body.readUtf8()) // the poll
        assertTrue(""""upload_id":"upl_1"""" in server.takeRequest().body.readUtf8())

        server.enqueue(json(202, """{"id":"run_1","status":"queued","sources":[]}"""))
        server.enqueue(json(200, """{"id":"run_1","status":"running","progress":{"stage":"matching","percent":40,"message":"Conferindo títulos"}}"""))
        server.enqueue(json(200, """{"id":"run_1","status":"completed","summary":{"findings_total":2,"amount_at_risk":4900000}}"""))
        server.enqueue(json(200, """{"items":[{"id":"fnd_1","title":"Pagamento parcial","amount_at_risk":4900000}],"next_cursor":"c2"}"""))
        server.enqueue(json(200, """{"items":[{"id":"fnd_2","title":"Duplicado","amount_at_risk":0}],"next_cursor":null}"""))

        val run = reconciler(dm).start(prepared, steps::add)
        val done = reconciler(dm).follow(run.id, steps::add)
        assertEquals(RunStatus.COMPLETED, done.status)
        assertEquals(Step.Running("Conferindo títulos"), steps.last())
        assertEquals(listOf("fnd_1", "fnd_2"), dm.api.findings(run.id).map { it.id })
    }

    @Test
    fun `a file the scanner refused stops the flow with the reason`() = runTest {
        val dm = newClient(server)
        val presigned = server.url("/presigned/bucket").toString()
        server.enqueue(json(201, """{"id":"upl_2","method":"POST","url":"$presigned","fields":{}}"""))
        server.enqueue(MockResponse().setResponseCode(204))
        server.enqueue(json(200, """{"id":"upl_2","file_name":"x.csv","status":"rejected","rejected_reason":"malware"}"""))
        val e =
            assertFailsWith<ApiException> {
                reconciler(dm).prepare(listOf(LocalFile.of("x.csv", "a;b\n".encodeToByteArray())), RunOptions())
            }
        assertEquals(ApiException.UPLOAD_REJECTED, e.code)
        assertTrue("x.csv" in e.message && "malware" in e.message, e.message)
    }

    @Test
    fun `a failed presigned upload is an error, not a silent success`() = runTest {
        val dm = newClient(server)
        val presigned = server.url("/presigned/bucket").toString()
        server.enqueue(json(201, """{"id":"upl_3","method":"POST","url":"$presigned","fields":{}}"""))
        server.enqueue(MockResponse().setResponseCode(403).setBody("<Error>AccessDenied</Error>"))
        val e = assertFailsWith<ApiException> { reconciler(dm).upload(LocalFile.of("y.csv", "a;b\n".encodeToByteArray())) }
        assertEquals(403, e.status)
    }

    @Test
    fun `media goes up as a PUT without a hash`() = runTest {
        val dm = newClient(server)
        val presigned = server.url("/presigned/media").toString()
        server.enqueue(json(201, """{"id":"upl_m","method":"PUT","url":"$presigned","headers":{"Content-Type":"audio/mpeg"}}"""))
        server.enqueue(MockResponse().setResponseCode(200))
        server.enqueue(json(200, """{"id":"upl_m","file_name":"nota.mp3","status":"ready"}"""))
        val audio = byteArrayOf(0x49, 0x44, 0x33, 4, 0, 0, 0, 0, 0, 0) + ByteArray(2048) { 7 }
        reconciler(dm).upload(LocalFile.of("nota.mp3", audio))
        val create = server.takeRequest().body.readUtf8()
        assertTrue(""""kind":"media"""" in create && """"mime":"audio/mpeg"""" in create && "sha256" !in create, create)
        val put = server.takeRequest()
        assertEquals("PUT", put.method)
        assertEquals(audio.size.toLong(), put.bodySize)
        assertEquals("{}", server.takeRequest().body.readUtf8())
    }

    @Test
    fun `a run waiting for approval stops the polling and resumes once approved`() = runTest {
        val dm = newClient(server)
        server.enqueue(json(200, """{"id":"run_9","status":"running"}"""))
        server.enqueue(json(200, """{"id":"run_9","status":"needs_approval","pending_quote":{"id":"quo_2","expected_credits":90,"cap_credits":120,"signature":"v1=ab"}}"""))
        val parked = reconciler(dm).follow("run_9")
        assertEquals(RunStatus.NEEDS_APPROVAL, parked.status)
        assertEquals(120, parked.pendingQuote!!.capCredits)
        server.takeRequest()
        server.takeRequest()
        server.enqueue(json(200, """{"id":"run_9","status":"running"}"""))
        dm.api.approveQuote("run_9", parked.pendingQuote!!, "key-1")
        val approve = server.takeRequest()
        assertEquals("/v1/runs/run_9/approve-quote", approve.path)
        assertEquals("key-1", approve.getHeader("Idempotency-Key"))
        assertEquals("""{"quote_id":"quo_2","quote_signature":"v1=ab"}""", approve.body.readUtf8())
    }

    @Test
    fun `confirming the same estimate twice sends the same key`() = runTest {
        val dm = newClient(server)
        val prepared = Prepared(listOf("upl_1"), Quote("quo_1", 40, 50, "sig"), RunOptions())
        server.enqueue(MockResponse().setResponseCode(504))
        assertFailsWith<ApiException> { reconciler(dm).start(prepared) }
        server.enqueue(json(202, """{"id":"run_1","status":"queued"}"""))
        reconciler(dm).start(prepared)
        val first = server.takeRequest().getHeader("Idempotency-Key")
        assertEquals(first, server.takeRequest().getHeader("Idempotency-Key"), "um orçamento aceito vira uma execução só")
    }
}
