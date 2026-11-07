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
    fun invitationInboxNeedsExplicitResponseAndNeverUsesSelectedCompany() = runBlocking {
        val preview = """{"id":"inv_a","organization":"Equipe","email_hint":"a***@example.test","role":"uploader","status":"pending","expires_at":"2026-11-13T12:00:00Z","email_matches":true}"""
        answer("""{"items":[$preview],"next_cursor":"inv_a"}""")
        val inbox = InvitationInboxViewModel(client().inAccount("org_old"))
        inbox.load()
        until { !inbox.state.value.busy }
        val list = server.takeRequest(1, TimeUnit.SECONDS)!!
        assertEquals("/v1/me/invitations", list.path)
        assertNull(list.getHeader("X-Docmatch-Org"))
        assertEquals(1, inbox.state.value.items.size)
        assertEquals(1, server.requestCount)
        val other = preview.replace("inv_a", "inv_b")
        answer("""{"items":[$other]}""")
        inbox.load(true)
        until { !inbox.state.value.busy }
        assertEquals("/v1/me/invitations?cursor=inv_a", server.takeRequest(1, TimeUnit.SECONDS)!!.path)
        assertEquals(2, inbox.state.value.items.size)
        assertNull(inbox.state.value.nextCursor)
        answer("""{"organization_id":"org_team","accepted":true}""")
        inbox.respond("inv_a", true)
        until { !inbox.state.value.busy }
        val accepted = server.takeRequest(1, TimeUnit.SECONDS)!!
        assertEquals("/v1/me/invitations/inv_a/respond", accepted.path)
        assertNull(accepted.getHeader("X-Docmatch-Org"))
        assertEquals("""{"accept":true}""", accepted.body.readUtf8())
        assertEquals(listOf("inv_b"), inbox.state.value.items.map { it.id })
        inbox.respond("inv_a", true)
        assertEquals(3, server.requestCount)
        answer("""{"items":[]}""")
        inbox.load()
        until { !inbox.state.value.busy }
        server.takeRequest(1, TimeUnit.SECONDS)
        assertTrue(inbox.state.value.items.isEmpty())
    }

    @Test
    fun rememberedAccountRequiresSameIdentityOriginAndMembership() = runBlocking {
        for (scenario in listOf("restart", "identity", "origin", "membership", "logout")) {
            val store = MemorySecretStore()
            answer(both)
            answer(me)
            val session = SessionViewModel(client(), store)
            until { session.state.value is SessionState.SignedIn }
            repeat(2) { server.takeRequest(1, TimeUnit.SECONDS) }
            answer(both)
            answer(me)
            session.selectAccount("org_company")
            until { session.accounts.value.selected == "org_company" }
            repeat(2) { server.takeRequest(1, TimeUnit.SECONDS) }
            if (scenario == "logout") {
                answer("{}")
                session.signedOut()
                until { session.state.value is SessionState.SignedOut }
                server.takeRequest(1, TimeUnit.SECONDS)
                assertNull(store.read())
            }
            val available = when (scenario) {
                "identity" -> both.replace("usr_1", "usr_2")
                "membership" -> onlyPersonal
                else -> both
            }
            answer(available)
            answer(me)
            val origin = if (scenario == "origin") server.url("/").toString().replace("localhost", "127.0.0.1") else server.url("/").toString()
            val reopened = SessionViewModel(Docmatch(origin, MemorySecretStore(), "test", "{}"), store)
            until { reopened.state.value is SessionState.SignedIn }
            assertEquals(scenario, if (scenario == "restart") "org_company" else "org_personal", reopened.accounts.value.selected)
            assertNull(server.takeRequest(1, TimeUnit.SECONDS)?.getHeader("X-Docmatch-Org"))
            assertEquals(reopened.accounts.value.selected, server.takeRequest(1, TimeUnit.SECONDS)?.getHeader("X-Docmatch-Org"))
        }
    }

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
