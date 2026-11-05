package br.com.docmatch.app.ui

import android.content.Context
import android.content.Intent
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.core.content.FileProvider
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Decision
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Evidence
import br.com.docmatch.core.Finding
import br.com.docmatch.core.Format
import br.com.docmatch.core.Run
import br.com.docmatch.core.RunStatus
import br.com.docmatch.core.Step
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

sealed interface RunState {
    data object Loading : RunState

    data class Running(val step: Step?) : RunState

    data class Done(val run: Run, val findings: List<Finding>) : RunState

    data class Failed(val message: String) : RunState
}

class RunViewModel(private val dm: Docmatch, private val runId: String) : ViewModel() {
    private val _state = MutableStateFlow<RunState>(RunState.Loading)
    val state: StateFlow<RunState> = _state
    private val _notice = MutableStateFlow<String?>(null)
    val notice: StateFlow<String?> = _notice

    init {
        load()
    }

    fun load() {
        viewModelScope.launch {
            _state.value = RunState.Loading
            _state.value =
                try {
                    val run = dm.reconciler.await(runId) { _state.value = RunState.Running(it) }
                    if (run.status == RunStatus.COMPLETED) {
                        RunState.Done(run, dm.api.findings(run.id).sortedByDescending { it.amountAtRisk })
                    } else {
                        RunState.Failed(dm.texts.t("results.live_failed", mapOf("message" to (run.error?.message ?: dm.texts.t("errors.unknown")))))
                    }
                } catch (e: ApiException) {
                    RunState.Failed(e.message)
                }
        }
    }

    /** Records the decision on the API (the site and the other devices see it) and here. */
    fun decide(finding: Finding, decision: String) {
        val done = _state.value as? RunState.Done ?: return
        viewModelScope.launch {
            try {
                dm.api.feedback(runId, finding.id, decision)
                _state.value = done.copy(findings = done.findings.map { if (it.id == finding.id) it.copy(status = decision) else it })
                _notice.value = dm.texts.t("mobile.finding_decided")
            } catch (e: ApiException) {
                _notice.value = e.message
            }
        }
    }

    /** Downloads the report and hands it to Android's share sheet. */
    fun export(context: Context, format: String) {
        viewModelScope.launch {
            try {
                val bytes = dm.api.export(runId, format)
                val file =
                    withContext(Dispatchers.IO) {
                        File(context.cacheDir, "exports").apply { mkdirs() }.resolve("docmatch-$runId.$format").apply { writeBytes(bytes) }
                    }
                val uri = FileProvider.getUriForFile(context, "${context.packageName}.exports", file)
                val mime = if (format == "pdf") "application/pdf" else "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
                val send = Intent(Intent.ACTION_SEND).setType(mime).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                context.startActivity(Intent.createChooser(send, dm.texts.t("mobile.export")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            } catch (e: ApiException) {
                _notice.value = dm.texts.t("mobile.export_failed", mapOf("message" to e.message))
            }
        }
    }

    fun dismissNotice() {
        _notice.value = null
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RunScreen(vm: RunViewModel) {
    val state by vm.state.collectAsState()
    val notice by vm.notice.collectAsState()
    var open by remember { mutableStateOf<Finding?>(null) }
    val context = LocalContext.current

    when (val s = state) {
        RunState.Loading -> Loading(t("results.live_loading"))
        is RunState.Running ->
            Column(Modifier.fillMaxSize().padding(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                Text(t("mobile.run_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
                LinearProgressIndicator(Modifier.fillMaxWidth())
                Text(s.step?.let { stepLabel(it) } ?: t("mobile.run_waiting"), modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
            }
        is RunState.Failed -> ErrorState(s.message, vm::load)
        is RunState.Done -> {
            val names = s.run.sources.associate { it.id to it.fileName }
            LazyColumn(contentPadding = PaddingValues(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                item {
                    Text(t("mobile.run_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
                    Text(t("mobile.run_findings", mapOf("count" to s.findings.size)))
                    Text(t("mobile.run_at_risk", mapOf("amount" to Format.money(s.findings.sumOf { it.amountAtRisk }))), fontWeight = FontWeight.SemiBold)
                    Row(horizontalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                        OutlinedButton(onClick = { vm.export(context, "xlsx") }) { Text(t("results.export_xlsx")) }
                        OutlinedButton(onClick = { vm.export(context, "pdf") }) { Text(t("results.export_pdf")) }
                    }
                    notice?.let {
                        Text(it, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    }
                }
                if (s.findings.isEmpty()) item { Text(t("mobile.run_clean")) }
                items(s.findings, key = { it.id }) { f -> FindingCard(f) { open = f } }
            }
            open?.let { f ->
                val current = s.findings.firstOrNull { it.id == f.id } ?: f
                ModalBottomSheet(onDismissRequest = { open = null }) {
                    FindingDetail(current, names, onDecide = { d -> vm.decide(current, d) }, onClose = { open = null })
                }
            }
        }
    }
}

@Composable
private fun FindingCard(f: Finding, onOpen: () -> Unit) {
    Card(Modifier.fillMaxWidth().testTag(TAG_FINDING).clickable(onClick = onOpen)) {
        Column(Modifier.padding(DmSize.space2), verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
            Text(f.title, fontWeight = FontWeight.SemiBold)
            Text(Format.money(f.amountAtRisk))
            Row(horizontalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                Text(confidenceLabel(f), color = MaterialTheme.colorScheme.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
                Text(statusLabel(f.status), color = MaterialTheme.colorScheme.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

@Composable
private fun FindingDetail(f: Finding, names: Map<String, String>, onDecide: (String) -> Unit, onClose: () -> Unit) {
    Column(
        Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(DmSize.space3).navigationBarsPadding(),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        Text(f.title, style = MaterialTheme.typography.titleLarge, modifier = Modifier.semantics { heading() })
        Text(t("results.amount_at_risk") + ": " + Format.money(f.amountAtRisk), fontWeight = FontWeight.SemiBold)
        if (f.explanation.isNotBlank()) {
            Text(t("results.explanation"), fontWeight = FontWeight.SemiBold)
            Text(f.explanation)
        }
        Text(t("mobile.finding_evidence"), fontWeight = FontWeight.SemiBold)
        f.evidence.groupBy { it.sourceId }.forEach { (source, items) ->
            Text(names[source] ?: source, style = MaterialTheme.typography.labelLarge)
            items.forEach { EvidenceRow(it) }
            HorizontalDivider()
        }
        Row(horizontalArrangement = Arrangement.spacedBy(DmSize.space2)) {
            Button(onClick = { onDecide(Decision.CONFIRMED) }) { Text(t("results.accept")) }
            OutlinedButton(onClick = { onDecide(Decision.DISMISSED) }) { Text(t("results.reject")) }
            TextButton(onClick = onClose) { Text(t("common.close")) }
        }
    }
}

@Composable
private fun EvidenceRow(e: Evidence) {
    val v = e.values
    Column(Modifier.fillMaxWidth().padding(vertical = DmSize.space1)) {
        val line = listOfNotNull(Format.date(v?.date).ifEmpty { null }, v?.description ?: e.label ?: e.excerpt).joinToString(" · ")
        Text(line)
        v?.amount?.let { Text(Format.money(it), fontWeight = FontWeight.SemiBold) }
        v?.document?.let { Text(it, style = MaterialTheme.typography.bodySmall) }
    }
}

@Composable
private fun confidenceLabel(f: Finding): String =
    when (f.confidenceLabel) {
        "confirmed" -> t("results.confidence_high")
        "probable" -> t("results.confidence_medium")
        else -> t("results.confidence_low")
    }

@Composable
fun statusLabel(status: String): String =
    when (status) {
        Decision.CONFIRMED -> t("results.status_accepted")
        Decision.DISMISSED -> t("results.status_rejected")
        Decision.RESOLVED -> t("results.status_edited")
        else -> t("results.status_open")
    }
