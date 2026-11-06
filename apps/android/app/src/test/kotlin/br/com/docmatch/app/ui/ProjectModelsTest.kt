package br.com.docmatch.app.ui

import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.MemorySecretStore
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withTimeout
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class ProjectModelsTest {
    private lateinit var server: MockWebServer

    @Before
    fun start() {
        Dispatchers.setMain(UnconfinedTestDispatcher())
        server = MockWebServer()
        server.start()
    }

    @After
    fun stop() {
        server.shutdown()
        Dispatchers.resetMain()
    }

    private fun answer(body: String, status: Int = 200) {
        server.enqueue(
            MockResponse()
                .setResponseCode(status)
                .setHeader("Content-Type", "application/json")
                .setBody(body),
        )
    }

    private fun client() = Docmatch(server.url("/").toString(), MemorySecretStore(), "test", "{}")

    private suspend fun until(predicate: () -> Boolean) {
        withTimeout(5000) { while (!predicate()) delay(10) }
    }

    private val me =
        """{"user":{"id":"usr_1","email":"test@example.test","display_name":"Test"},"org":{"id":"org_personal","name":"Pessoal"}}"""
    private val personal =
        """{"id":"org_personal","name":"Pessoal","kind":"personal","role":"owner","plan":"starter","capabilities":{"manage_account":true,"create_project":true,"reconcile":true,"read_results":true,"upload":true}}"""
    private val company =
        """{"id":"org_company","name":"Empresa","kind":"team","role":"uploader","plan":"pro","capabilities":{"upload":true}}"""
    private val both
        get() = """{"user_id":"usr_1","items":[$personal,$company]}"""

    private val onlyPersonal
        get() = """{"user_id":"usr_1","items":[$personal]}"""

    private val project =
        """{"id":"prj_test","name":"Janeiro","visibility":"private","version":1,"revision":1,"document_count":0,"created_at":"2026-11-06T10:00:00Z","updated_at":"2026-11-06T10:00:00Z","capabilities":{"reconcile":true,"read_results":true,"upload":true},"documents":[]}"""

    @Test
    fun switchingBindsNewRequestsAndRemovalReturnsToPersonal() = runBlocking {
        answer(both)
        answer(me)
        val session = SessionViewModel(client())
        until { session.state.value is SessionState.SignedIn }
        val original = session.currentDM
        repeat(2) { assertNotNull(server.takeRequest(1, TimeUnit.SECONDS)) }
        answer(both)
        answer(me)
        session.selectAccount("org_company")
        until { session.accounts.value.selected == "org_company" }
        assertEquals("uploader", session.accounts.value.current?.role)
        assertEquals("org_personal", original.api.organizationId)
        assertNull(server.takeRequest(1, TimeUnit.SECONDS)?.getHeader("X-Docmatch-Org"))
        assertEquals(
            "org_company",
            server.takeRequest(1, TimeUnit.SECONDS)?.getHeader("X-Docmatch-Org"),
        )
        answer(onlyPersonal)
        answer(onlyPersonal)
        answer(me)
        session.validateAccounts()
        until { session.accounts.value.selected == "org_personal" }
        assertEquals("owner", session.accounts.value.current?.role)
    }

    @Test
    fun delayedAccountSelectionCannotRestoreSignedOutSession() = runBlocking {
        answer(both)
        answer(me)
        val session = SessionViewModel(client())
        until { session.state.value is SessionState.SignedIn }
        repeat(2) { server.takeRequest(1, TimeUnit.SECONDS) }
        answer(both)
        server.enqueue(
            MockResponse()
                .setHeader("Content-Type", "application/json")
                .setBody(me)
                .setBodyDelay(250, TimeUnit.MILLISECONDS),
        )
        session.selectAccount("org_company")
        repeat(2) { assertNotNull(server.takeRequest(1, TimeUnit.SECONDS)) }
        answer("{}")
        session.signedOut()
        until { session.state.value is SessionState.SignedOut }
        delay(400)
        assertTrue(session.state.value is SessionState.SignedOut)
        assertNull(session.accounts.value.selected)
        assertTrue(session.accounts.value.items.isEmpty())
    }

    @Test
    fun conflictKeepsDraftAndNeverOverwritesOtherEditor() = runBlocking {
        answer(project)
        answer("""{"items":[]}""")
        answer("""{"items":[]}""")
        val model = ProjectViewModel(client().inAccount("org_company"), "prj_test")
        until { model.state.value.project != null }
        repeat(3) { server.takeRequest(1, TimeUnit.SECONDS) }
        answer("""{"code":"conflict","message":"O projeto mudou."}""", 409)
        model.rename("Minha edição")
        model.retrySave()
        until { model.state.value.save == ProjectSaveState.CONFLICT }
        assertEquals("Minha edição", model.state.value.name)
        assertEquals("Janeiro", model.state.value.project?.name)
        assertTrue(model.state.value.dirty)
        val write = server.takeRequest(1, TimeUnit.SECONDS)!!
        assertEquals("org_company", write.getHeader("X-Docmatch-Org"))
        assertTrue(write.body.readUtf8().contains("\"version\":1"))
        model.retrySave()
        delay(800)
        assertNull(server.takeRequest(100, TimeUnit.MILLISECONDS))
        model.discardPendingEdits()
    }

    @Test
    fun unchangedResultOpensWithoutASecondRunAndRevocationClearsIt() = runBlocking {
        answer(project)
        answer("""{"items":[]}""")
        answer("""{"items":[]}""")
        val model = ProjectViewModel(client(), "prj_test")
        until { model.state.value.project != null }
        repeat(3) { server.takeRequest(1, TimeUnit.SECONDS) }
        answer("""{"state":"unchanged","revision":1,"run":{"id":"run_old","status":"completed"}}""")
        model.prepare()
        until { model.state.value.openedRun != null }
        assertEquals("run_old", model.state.value.openedRun)
        assertNull(model.state.value.analysis)
        assertEquals(
            "/v1/projects/prj_test/runs/estimate",
            server.takeRequest(1, TimeUnit.SECONDS)?.path,
        )
        model.approve()
        assertNull(server.takeRequest(100, TimeUnit.MILLISECONDS))
        answer("""{"code":"forbidden","message":"Sem acesso."}""", 403)
        model.load()
        until { model.state.value.error != null }
        assertNull(model.state.value.project)
        assertTrue(model.state.value.runs.isEmpty())
    }
}
