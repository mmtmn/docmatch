package br.com.docmatch.app.ui

import android.content.Context
import android.net.Uri
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.*
import java.util.UUID
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

enum class ProjectSaveState {
    SAVED,
    PENDING,
    SAVING,
    OFFLINE,
    CONFLICT,
}

data class ProjectState(
    val project: SavedProject? = null,
    val name: String = "",
    val settings: ProjectSettings = ProjectSettings(),
    val runs: List<Run> = emptyList(),
    val deleted: Boolean = false,
    val nextRuns: String? = null,
    val events: List<ProjectEvent> = emptyList(),
    val busy: Boolean = false,
    val dirty: Boolean = false,
    val save: ProjectSaveState = ProjectSaveState.SAVED,
    val analysis: ProjectAnalysis? = null,
    val openedRun: String? = null,
    val error: String? = null,
    val drafts: List<ProjectDraft> = emptyList(),
    val localSaved: Boolean = false,
    val recoveryFailed: Boolean = false,
) {
    val canEdit
        get() = project?.capabilities?.reconcile == true && project.archivedAt == null
}

class ProjectViewModel(dm: Docmatch, val id: String, private val recovery: ProjectDraftEditor? = null) : ViewModel() {
    val dm = dm.inProject(id)
    private val _state = MutableStateFlow(ProjectState())
    val state: StateFlow<ProjectState> = _state
    private val writes = Mutex()
    private var debounce: Job? = null
    private var approvalKey = ""
    private var reprocessReason = ""
    private var recoveryDebounce: Job? = null
    private val restoredDrafts = mutableListOf<ProjectDraft>()

    init {
        load()
    }

    fun load(discardEdits: Boolean = false) {
        if (
            state.value.busy ||
                state.value.save == ProjectSaveState.SAVING ||
                state.value.dirty && !discardEdits
        )
            return
        if (discardEdits) preserveDraft()
        viewModelScope.launch {
            _state.update { it.copy(busy = true, error = null) }
            try {
                val item = dm.api.project(id)
                val runs = if (item.capabilities.readResults) dm.api.runs() else RunList()
                val events = dm.api.projectActivity(id).items
                _state.value =
                    ProjectState(
                        project = item,
                        name = item.name,
                        settings = item.settings ?: ProjectSettings(),
                        runs = runs.items,
                        nextRuns = runs.nextCursor,
                        events = events,
                        drafts = if (item.capabilities.reconcile && item.archivedAt == null) recovery?.list().orEmpty() else emptyList(),
                    )
            } catch (e: ApiException) {
                fail(e)
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun validateAccess() {
        if (state.value.busy || state.value.save == ProjectSaveState.SAVING) return
        viewModelScope.launch {
            try {
                val latest = dm.api.project(id)
                _state.update {
                    if (it.dirty)
                        it.copy(
                            save =
                                if (latest.version != it.project?.version) ProjectSaveState.CONFLICT
                                else it.save,
                        )
                    else
                        it.copy(
                            project = latest,
                            name = latest.name,
                            settings = latest.settings ?: ProjectSettings(),
                            runs = if (latest.capabilities.readResults) it.runs else emptyList(),
                            analysis = null,
                        )
                }
                if (state.value.save == ProjectSaveState.CONFLICT) debounce?.cancel()
            } catch (e: ApiException) {
                fail(e)
            }
        }
    }

    fun discardPendingEdits() {
        if (state.value.save == ProjectSaveState.SAVING) return
        debounce?.cancel()
        recoveryDebounce?.cancel()
        recovery?.remove()
        _state.update {
            it.copy(
                dirty = false,
                save = ProjectSaveState.SAVED,
                name = it.project?.name.orEmpty(),
                settings = it.project?.settings ?: ProjectSettings(),
                localSaved = false,
            )
        }
    }

    fun add(context: Context, uris: List<Uri>, replacesId: String? = null) {
        if (state.value.busy) return
        if (replacesId != null && uris.size != 1) {
            _state.update { it.copy(error = dm.texts.t("projects.replace_one")) }
            return
        }
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val (files, problems) = PickedFiles.check(context, uris, dm.texts)
                _state.update { it.copy(busy = false) }
                if (problems.isEmpty()) upload(files, replacesId)
                else _state.update { it.copy(error = problems.joinToString("\n")) }
            } finally {
                if (state.value.error != null) _state.update { it.copy(busy = false) }
            }
        }
    }

    fun applyReplacement(doc: ProjectDocument) = operation {
        val current = state.value.project ?: return@operation
        val old = doc.pendingReplacementId ?: return@operation
        val saved = dm.api.replaceProjectDocument(id, old, doc.id, current.version)
        _state.update { it.copy(project = saved) }
    }

    fun rename(name: String) {
        edit { it.copy(name = name) }
    }

    fun settings(settings: ProjectSettings) {
        edit { it.copy(settings = settings) }
    }

    private fun edit(change: (ProjectState) -> ProjectState) {
        if (!state.value.canEdit || state.value.busy) return
        _state.update {
            change(it)
                .copy(
                    dirty = true,
                    analysis = null,
                    save =
                        if (it.save == ProjectSaveState.CONFLICT) it.save
                        else ProjectSaveState.PENDING,
                )
        }
        recoveryDebounce?.cancel()
        recoveryDebounce = viewModelScope.launch { delay(100); preserveDraft() }
        if (state.value.save == ProjectSaveState.CONFLICT) return
        debounce?.cancel()
        debounce = viewModelScope.launch {
            delay(600)
            // Later keystrokes cancel the delay, never an already sent versioned write.
            viewModelScope.launch { flush() }
        }
    }

    fun retrySave() {
        viewModelScope.launch { flush() }
    }

    private suspend fun flush(): Boolean = writes.withLock {
        if (state.value.save == ProjectSaveState.CONFLICT) return@withLock false
        preserveDraft()
        while (state.value.dirty) {
            val sent = state.value
            val project = sent.project ?: return@withLock false
            _state.update { it.copy(save = ProjectSaveState.SAVING) }
            try {
                val saved =
                    dm.api.updateProject(
                        id,
                        project.version,
                        name = sent.name,
                        settings = sent.settings,
                    )
                _state.update {
                    val dirty = it.name != sent.name || it.settings != sent.settings
                    it.copy(
                        project = saved,
                        dirty = dirty,
                        save = if (dirty) ProjectSaveState.PENDING else ProjectSaveState.SAVED,
                        error = null,
                    )
                }
                if (state.value.dirty) preserveDraft()
                else {
                    recovery?.remove()
                    restoredDrafts.forEach { recovery?.remove(it) }
                    restoredDrafts.clear()
                    _state.update { it.copy(drafts = recovery?.list().orEmpty(), localSaved = false) }
                }
            } catch (e: ApiException) {
                fail(e)
                _state.update {
                    it.copy(
                        save =
                            if (e.status == 409) ProjectSaveState.CONFLICT
                            else ProjectSaveState.OFFLINE,
                    )
                }
                return@withLock false
            }
        }
        true
    }

    fun preserveDraft() {
        recoveryDebounce?.cancel()
        recoveryDebounce = null
        val current = state.value
        val project = current.project ?: return
        val editor = recovery ?: return
        if (!current.dirty) return
        val saved = editor.save(project.version, current.name, current.settings)
        _state.update { it.copy(localSaved = saved, recoveryFailed = !saved) }
    }

    fun restoreDraft(draft: ProjectDraft) {
        val current = state.value
        if (current.busy || current.dirty || !current.canEdit || current.save == ProjectSaveState.SAVING) return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val fresh = dm.api.project(id)
                if (fresh.version != current.project?.version) {
                    _state.update { it.copy(project = fresh, name = fresh.name, settings = fresh.settings ?: ProjectSettings(), error = dm.texts.t("projects.recovery_refresh")) }
                    return@launch
                }
                restoredDrafts.add(draft)
                _state.update { it.copy(name = draft.name, settings = draft.settings, dirty = true, save = ProjectSaveState.PENDING) }
                flush()
            } catch (e: ApiException) { fail(e) }
            finally { _state.update { it.copy(busy = false) } }
        }
    }

    fun discardDraft(draft: ProjectDraft) {
        if (state.value.busy || state.value.save == ProjectSaveState.SAVING) return
        val removed = recovery?.remove(draft) == true
        _state.update { it.copy(drafts = recovery?.list().orEmpty(), recoveryFailed = !removed) }
    }

    fun upload(files: List<LocalFile>, replacesId: String? = null) = operation {
        if (state.value.project?.capabilities?.upload != true) return@operation
        for (file in files) {
            try {
                val upload = dm.reconciler.upload(file, replacesId = replacesId)
                var latest = dm.api.project(id)
                if (replacesId != null) {
                    val added = latest.documents?.firstOrNull { it.uploadId == upload.id }
                    if (added != null)
                        latest =
                            dm.api.replaceProjectDocument(id, replacesId, added.id, latest.version)
                }
                _state.update { it.copy(project = latest) }
            } catch (e: ApiException) {
                try {
                    val latest = dm.api.project(id)
                    _state.update { it.copy(project = latest) }
                } catch (_: ApiException) {}
                throw e
            }
        }
    }

    fun remove(doc: ProjectDocument) = operation {
        val current = state.value.project ?: return@operation
        dm.api.removeProjectDocument(id, doc.id, current.version)
        val saved = dm.api.project(id)
        _state.update { it.copy(project = saved) }
    }

    fun prepare(reason: String = "") = operation {
        val current = state.value.project ?: return@operation
        reprocessReason = reason
        val analysis = dm.api.estimateProject(id, current.revision, reason)
        approvalKey = UUID.randomUUID().toString()
        _state.update {
            it.copy(
                analysis = if (analysis.run == null) analysis else null,
                openedRun = analysis.run?.id,
            )
        }
    }

    fun approve() {
        val analysis = state.value.analysis ?: return
        val quote = analysis.quote ?: return
        if (state.value.busy || state.value.dirty) return
        operation(clearAnalysis = false) {
            val result =
                dm.api.reconcileProject(id, analysis.revision, quote, approvalKey, reprocessReason)
            _state.update { it.copy(openedRun = result.run?.id, analysis = null) }
        }
    }

    fun dismissApproval() {
        _state.update { it.copy(analysis = null) }
    }

    fun openedRunShown() {
        _state.update { it.copy(openedRun = null) }
    }

    fun moreRuns() =
        operation(clearAnalysis = false) {
            val next = state.value.nextRuns ?: return@operation
            val page = dm.api.runs(cursor = next)
            _state.update {
                it.copy(
                    runs = (it.runs + page.items).distinctBy { run -> run.id },
                    nextRuns = page.nextCursor,
                )
            }
        }

    fun delete() = operation {
        val current = state.value.project ?: return@operation
        dm.api.deleteProject(id, current.version)
        _state.update { it.copy(deleted = true) }
    }

    fun share(visibility: String, users: List<String>) = operation {
        val current = state.value.project ?: return@operation
        val saved = dm.api.shareProject(id, current.version, visibility, users)
        _state.update { it.copy(project = saved) }
    }

    fun archive(value: Boolean) = operation {
        val current = state.value.project ?: return@operation
        val saved = dm.api.updateProject(id, current.version, archived = value)
        _state.update { it.copy(project = saved) }
    }

    private fun operation(clearAnalysis: Boolean = true, block: suspend () -> Unit) {
        if (state.value.busy) return
        _state.update {
            it.copy(busy = true, error = null, analysis = if (clearAnalysis) null else it.analysis)
        }
        viewModelScope.launch {
            try {
                if (flush()) block()
            } catch (e: ApiException) {
                fail(e)
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    private fun fail(e: ApiException) {
        if (e.status in listOf(401, 403, 404)) _state.value = ProjectState(error = e.message)
        else _state.update { it.copy(error = e.message) }
    }
}
