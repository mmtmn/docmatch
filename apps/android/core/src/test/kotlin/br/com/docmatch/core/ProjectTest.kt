package br.com.docmatch.core

import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockWebServer
import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class ProjectTest {
    @Test
    fun accountAndProjectSurviveRefreshWithoutChangingAnotherClient() = runTest {
        MockWebServer().use { server ->
            server.start()
            val root = newClient(server)
            server.enqueue(json(200, SESSION_JSON).addHeader("Set-Cookie", sessionCookie()).addHeader("Set-Cookie", refreshCookie()))
            root.api.devLogin()
            server.takeRequest()
            val company = root.inAccount("org_company").inProject("prj_test")
            val personal = root.inAccount("org_personal")
            server.enqueue(json(401, """{"code":"session_expired","message":"Entre de novo"}"""))
            server.enqueue(json(204, "").addHeader("Set-Cookie", sessionCookie("s1.new")))
            server.enqueue(json(200, """{"state":"unchanged","revision":4,"run":{"id":"run_1","status":"completed"}}"""))
            val result = company.api.reconcileProject("prj_test", 4, null, "retry-same-analysis")
            assertEquals("run_1", result.run?.id)
            assertNull(result.quote)
            val first = server.takeRequest()
            val refresh = server.takeRequest()
            val retry = server.takeRequest()
            for (request in listOf(first,retry)) {
                assertEquals("org_company", request.getHeader("X-Docmatch-Org"))
                assertEquals("/v1/projects/prj_test/runs", request.path)
                assertEquals("retry-same-analysis", request.getHeader("Idempotency-Key"))
            }
            assertEquals("/v1/auth/refresh",refresh.path)
            assertNull(refresh.getHeader("X-Docmatch-Org"))
            server.enqueue(json(200,SESSION_JSON))
            personal.api.me()
            assertEquals("org_personal",server.takeRequest().getHeader("X-Docmatch-Org"))
            server.enqueue(json(200,"""{"user_id":"usr_1","items":[]}"""))
            company.api.organizations()
            assertNull(server.takeRequest().getHeader("X-Docmatch-Org"))
        }
    }

    @Test
    fun projectUploadAndResultsUseBoundDestinationButStorageHasNoCredentials() = runTest {
        MockWebServer().use { server ->
            server.start()
            val dm = newClient(server).inAccount("org_company").inProject("prj_test")
            server.enqueue(json(200,"""{"id":"upl_1","method":"PUT","url":"${server.url("/object")}","headers":{},"fields":{},"expires_at":"2026-12-01T00:00:00Z"}"""))
            val file = LocalFile("bank.csv", "text/csv", 4, FileKind.CSV) { "a,b\n".byteInputStream() }
            val ticket = dm.api.createUpload(file,"hash")
            val create = server.takeRequest()
            assertEquals("/v1/projects/prj_test/uploads",create.path)
            assertEquals("org_company",create.getHeader("X-Docmatch-Org"))
            server.enqueue(json(200,""))
            dm.api.putObject(ticket,file)
            val stored = server.takeRequest()
            for (key in listOf("Cookie","Authorization","X-Docmatch-Org")) assertNull(stored.getHeader(key))
            server.enqueue(json(200,"""{"id":"upl_1","status":"ready","file_name":"bank.csv"}"""))
            dm.api.completeUpload("upl_1",null)
            val complete = server.takeRequest()
            assertEquals("/v1/projects/prj_test/uploads/upl_1/complete",complete.path)
            assertEquals("org_company",complete.getHeader("X-Docmatch-Org"))
            server.enqueue(json(200,"""{"items":[],"next_cursor":null}"""))
            dm.api.findings("run_1")
            assertEquals("/v1/projects/prj_test/runs/run_1/findings",server.takeRequest().requestUrl?.encodedPath)
        }
    }
}
