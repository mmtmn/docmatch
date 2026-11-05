package br.com.docmatch.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Format
import br.com.docmatch.core.Run
import br.com.docmatch.core.RunStatus
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

sealed interface HistoryState {
    data object Loading : HistoryState

    data class Ready(val runs: List<Run>, val next: String?) : HistoryState

    data class Failed(val message: String) : HistoryState
}

/** Every run of the account, whichever device started it. */
class HistoryViewModel(private val dm: Docmatch) : ViewModel() {
    private val _state = MutableStateFlow<HistoryState>(HistoryState.Loading)
    val state: StateFlow<HistoryState> = _state

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _state.value = HistoryState.Loading
            _state.value =
                try {
                    dm.api.runs().let { HistoryState.Ready(it.items, it.nextCursor) }
                } catch (e: ApiException) {
                    HistoryState.Failed(e.message)
                }
        }
    }

    fun more() {
        val ready = _state.value as? HistoryState.Ready ?: return
        val cursor = ready.next ?: return
        viewModelScope.launch {
            try {
                val page = dm.api.runs(cursor)
                _state.value = HistoryState.Ready(ready.runs + page.items, page.nextCursor)
            } catch (_: ApiException) {
                // Keep what is shown; the next tap tries again.
            }
        }
    }
}

@Composable
fun HistoryScreen(vm: HistoryViewModel, onOpen: (String) -> Unit) {
    val state by vm.state.collectAsState()
    when (val s = state) {
        HistoryState.Loading -> Loading(t("mobile.loading"))
        is HistoryState.Failed -> ErrorState(s.message, vm::refresh)
        is HistoryState.Ready ->
            LazyColumn(contentPadding = PaddingValues(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                item { Text(t("mobile.tab_history"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() }) }
                if (s.runs.isEmpty()) item { Text(t("mobile.history_empty")) }
                items(s.runs, key = { it.id }) { run ->
                    Card(Modifier.fillMaxWidth().clickable { onOpen(run.id) }) {
                        Column(Modifier.padding(DmSize.space2)) {
                            Text(runStatus(run.status), fontWeight = FontWeight.SemiBold)
                            Text(t("mobile.history_item", mapOf("date" to Format.dateTime(run.createdAt), "count" to run.sources.size)))
                            run.summary?.let { Text(t("mobile.run_findings", mapOf("count" to it.findingsTotal)) + " · " + Format.money(it.amountAtRisk)) }
                        }
                    }
                }
                if (s.next != null) item { TextButton(onClick = vm::more) { Text(t("mobile.loading")) } }
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
