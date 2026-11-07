package br.com.docmatch.core

import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class ProjectDraftsTest {
    private val scope = ProjectDraftScope("https://docmatch.test", "usr_1", "org_1", "prj_1")

    @Test fun `reopening preserves edits and all four scope fields isolate them`() {
        val store = MemorySecretStore()
        val journal = ProjectDraftJournal(store) { 100 }
        assertTrue(journal.editor(scope).save(1, "Recovered", ProjectSettings()))
        val reopened = ProjectDraftJournal(store) { 101 }
        assertEquals("Recovered", reopened.editor(scope).list().single().name)
        listOf(scope.copy(origin = "https://other.test"), scope.copy(userId = "usr_2"),
            scope.copy(accountId = "org_2"), scope.copy(projectId = "prj_2")).forEach {
            assertTrue(reopened.editor(it).list().isEmpty())
        }
    }

    @Test fun `a reviewed copy cannot delete subsequent edits in another window`() {
        val journal = ProjectDraftJournal(MemorySecretStore())
        val first = journal.editor(scope)
        val second = journal.editor(scope)
        assertTrue(first.save(1, "Earlier", ProjectSettings()))
        val reviewed = second.list().single()
        assertTrue(first.save(1, "Newer", ProjectSettings()))
        assertFalse(second.remove(reviewed))
        assertTrue(second.save(1, "Other window", ProjectSettings()))
        assertEquals(2, second.list().size)
        assertTrue(second.remove())
        assertEquals("Newer", first.list().single().name)
    }

    @Test fun `logout rejects delayed writes and new sessions start empty`() {
        val store = MemorySecretStore()
        val journal = ProjectDraftJournal(store)
        val old = journal.editor(scope)
        assertTrue(old.save(1, "Before logout", ProjectSettings()))
        assertTrue(journal.clear())
        assertFalse(old.save(1, "Late write", ProjectSettings()))
        assertTrue(old.list().isEmpty())
        assertTrue(ProjectDraftJournal(store).editor(scope).list().isEmpty())
        assertTrue(journal.editor(scope).save(2, "Next login", ProjectSettings()))
    }

    @Test fun `expiry and size cap preserve other unsaved edits`() {
        val store = MemorySecretStore()
        val journal = ProjectDraftJournal(store) { 100 }
        val editor = journal.editor(scope)
        assertTrue(editor.save(1, "Keep", ProjectSettings()))
        assertFalse(editor.save(1, "x".repeat(70_000), ProjectSettings()))
        assertEquals("Keep", editor.list().single().name)
        assertTrue(ProjectDraftJournal(store) { 100 + ProjectDraftJournal.TTL }.editor(scope).list().isEmpty())
    }

    @Test fun `storage failures are reported without crashing the editor`() {
        val store = object : SecretStore {
            override fun read(): ByteArray? = null
            override fun write(bytes: ByteArray?) { error("Unavailable") }
        }
        val journal = ProjectDraftJournal(store)
        assertFalse(journal.editor(scope).save(1, "Unsaved", ProjectSettings()))
        assertFalse(journal.clear())
    }
}
