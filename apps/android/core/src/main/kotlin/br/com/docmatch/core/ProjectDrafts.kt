package br.com.docmatch.core

import java.util.UUID
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json

@Serializable
data class ProjectDraftScope(val origin: String, val userId: String, val accountId: String, val projectId: String)

@Serializable
data class ProjectDraft(
    val id: String,
    val revision: String,
    val scope: ProjectDraftScope,
    val baseVersion: Long,
    val name: String,
    val settings: ProjectSettings,
    val savedAt: Long,
)

/** One shared journal backed by a separate encrypted KeystoreSecretStore value. */
class ProjectDraftJournal(private val store: SecretStore, private val now: () -> Long = System::currentTimeMillis) {
    private val json = Json { ignoreUnknownKeys = true }
    private var generation = 0
    private var records: List<ProjectDraft> = runCatching {
        store.read()?.let { json.decodeFromString<List<ProjectDraft>>(it.toString(Charsets.UTF_8)) } ?: emptyList()
    }.getOrDefault(emptyList()).filter { it.savedAt > now() - TTL }

    @Synchronized
    fun editor(scope: ProjectDraftScope) = ProjectDraftEditor(this, scope, generation)

    @Synchronized
    fun clear(): Boolean {
        generation++
        records = emptyList()
        return runCatching { store.write(null); store.read() == null }.getOrDefault(false)
    }

    @Synchronized
    internal fun list(editor: ProjectDraftEditor): List<ProjectDraft> =
        if (editor.generation != generation) emptyList()
        else records.filter { it.scope == editor.scope && it.savedAt > now() - TTL }.sortedByDescending { it.savedAt }

    @Synchronized
    internal fun save(editor: ProjectDraftEditor, version: Long, name: String, settings: ProjectSettings): Boolean {
        if (editor.generation != generation || version < 1) return false
        val draft = ProjectDraft(editor.id, UUID.randomUUID().toString(), editor.scope, version,
            name, settings.copy(ruleIds = settings.ruleIds.toList()), now())
        if (json.encodeToString(draft).toByteArray().size > 64 * 1024) return false
        val next = records.filter { it.id != editor.id && it.savedAt > now() - TTL }
        if (next.size >= 50) return false
        return persist(next + draft)
    }

    @Synchronized
    internal fun remove(editor: ProjectDraftEditor, draft: ProjectDraft?): Boolean {
        if (editor.generation != generation) return false
        val id = draft?.id ?: editor.id
        records.firstOrNull { it.id == id }?.let {
            if (it.scope != editor.scope || draft != null && it.revision != draft.revision) return false
        }
        return persist(records.filter { it.id != id && it.savedAt > now() - TTL })
    }

    private fun persist(next: List<ProjectDraft>): Boolean = runCatching {
        val data = json.encodeToString(next).toByteArray()
        if (data.size > 256 * 1024) return false
        store.write(data)
        if (store.read()?.contentEquals(data) != true) return false
        records = next
        true
    }.getOrDefault(false)

    companion object { const val TTL = 30L * 24 * 60 * 60 * 1000 }
}

class ProjectDraftEditor internal constructor(
    private val journal: ProjectDraftJournal,
    internal val scope: ProjectDraftScope,
    internal val generation: Int,
) {
    internal val id: String = UUID.randomUUID().toString()
    fun list(): List<ProjectDraft> = journal.list(this)
    fun save(version: Long, name: String, settings: ProjectSettings): Boolean = journal.save(this, version, name, settings)
    fun remove(draft: ProjectDraft? = null): Boolean = journal.remove(this, draft)
}
