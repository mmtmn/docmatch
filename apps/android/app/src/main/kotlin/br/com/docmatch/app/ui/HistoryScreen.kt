package br.com.docmatch.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.material.icons.Icons
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Format
import br.com.docmatch.core.Run
import br.com.docmatch.core.RunStatus
import br.com.docmatch.core.activity
import br.com.docmatch.core.SavedProject
import br.com.docmatch.core.importRunAsProject
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

sealed interface HistoryState {
    data object Loading : HistoryState

    data class Ready(val runs: List<Run>, val next: String?, val notice: String? = null) : HistoryState

    data class Failed(val message: String) : HistoryState
}

/** Every run of the account, whichever device started it. */
class HistoryViewModel(private val dm: Docmatch, private val projects: Boolean = false) : ViewModel() {
    private val _state = MutableStateFlow<HistoryState>(HistoryState.Loading)
    val state: StateFlow<HistoryState> = _state

    /** Reloads the first page; what is on screen stays while it loads. */
    suspend fun refresh() {
        if (_state.value !is HistoryState.Ready) _state.value = HistoryState.Loading
        _state.value =
            try {
                (if (projects) dm.api.activity() else dm.api.runs()).let { HistoryState.Ready(it.items, it.nextCursor) }
            } catch (e: ApiException) {
                if (e.status in listOf(401,403)) HistoryState.Failed(e.message) else (_state.value as? HistoryState.Ready)?.copy(notice = e.message) ?: HistoryState.Failed(e.message)
            }
    }

    suspend fun saveAsProject(runId: String, name: String): SavedProject = dm.api.importRunAsProject(runId, name)

    fun retry() {
        viewModelScope.launch { refresh() }
    }

    fun more() {
        val ready = _state.value as? HistoryState.Ready ?: return
        val cursor = ready.next ?: return
        viewModelScope.launch {
            _state.value =
                try {
                    val page = if (projects) dm.api.activity(cursor) else dm.api.runs(cursor)
                    // Offset pages shift when a run starts meanwhile: never show one twice.
                    ready.copy(runs = (ready.runs + page.items).distinctBy { it.id }, next = page.nextCursor, notice = null)
                } catch (_: ApiException) {
                    ready.copy(notice = dm.texts.t("mobile.load_more_failed"))
                }
        }
    }
}

@Composable
fun HistoryScreen(vm: HistoryViewModel, accountName: String = "", onSaved: (String) -> Unit = {}, onOpen: (Run) -> Unit) {
    var importing by remember { mutableStateOf<Run?>(null) }
    var name by remember { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    importing?.let { run ->
        AlertDialog(
            onDismissRequest = { if (!saving) importing = null },
            title = { Text(t("projects.import_action")) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                    Text(t("projects.import_explanation", mapOf("account" to accountName)))
                    OutlinedTextField(name, { name = it }, label = { Text(t("projects.name")) }, enabled = !saving, singleLine = true)
                    error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    saving = true
                    error = null
                    scope.launch {
                        try {
                            val project = vm.saveAsProject(run.id, name)
                            importing = null
                            onSaved(project.id)
                        } catch (e: ApiException) {
                            error = e.message
                        } finally {
                            saving = false
                        }
                    }
                }, enabled = !saving && name.isNotBlank()) { Text(t(if (saving) "projects.saving" else "projects.import_action")) }
            },
            dismissButton = { TextButton({ importing = null }, enabled = !saving) { Text(t("common.cancel")) } },
        )
    }
    val state by vm.state.collectAsState()
    // Every time the tab is shown: runs started on the site or another device appear.
    LaunchedEffect(Unit) { vm.refresh() }
    when (val s = state) {
        HistoryState.Loading -> Loading(t("mobile.loading"))
        is HistoryState.Failed -> ErrorState(s.message, vm::retry)
        is HistoryState.Ready ->
            LazyColumn(contentPadding = PaddingValues(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                item { DmHeading(t("mobile.tab_history"), t("mobile.history_text")) }
                if (s.runs.isEmpty()) item { DmEmptyState(DmIcons.Folder, t("mobile.tab_history"), t("mobile.history_empty")) }
                items(s.runs, key = { it.id }) { run ->
                    DmCard(Modifier.fillMaxWidth().clickable { onOpen(run) }) {
                        Column(Modifier.padding(DmSize.space2), verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
                            Text(runStatus(run.status), fontWeight = FontWeight.SemiBold)
                            Text(t("mobile.history_item", mapOf("date" to Format.dateTime(run.createdAt), "count" to run.sources.size)))
                            run.summary?.let { Text(t("mobile.run_findings", mapOf("count" to it.findingsTotal)) + " · " + Format.money(it.amountAtRisk)) }
                        }
                    }
                    if (run.canSaveAsProject) {
                        val suggestedName = t("projects.import_name", mapOf("date" to Format.dateTime(run.createdAt)))
                        TextButton({
                            name = suggestedName
                            error = null
                            importing = run
                        }) { Text(t("projects.import_action")) }
                    }
                }
                s.notice?.let { item { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) } }
                if (s.next != null) item { TextButton(onClick = vm::more) { Text(t("mobile.load_more")) } }
            }
    }
}

@Composable
fun runStatus(status: String): String =
    when (status) {
        RunStatus.QUEUED -> t("mobile.status_queued")
        RunStatus.RUNNING -> t("mobile.status_running")
        RunStatus.NEEDS_APPROVAL -> t("mobile.status_needs_approval")
        RunStatus.COMPLETED -> t("mobile.status_completed")
        RunStatus.FAILED -> t("mobile.status_failed")
        else -> t("mobile.status_cancelled")
    }
