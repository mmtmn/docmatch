package br.com.docmatch.app.ui

import android.content.Context
import android.content.Intent
import kotlinx.coroutines.CancellationException
import br.com.docmatch.core.ReconciledReceivable
import br.com.docmatch.core.findingPriority
import androidx.compose.foundation.clickable
import androidx.compose.animation.animateContentSize
import androidx.compose.ui.text.font.FontFamily
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
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.unit.dp
import androidx.compose.material.icons.Icons
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.core.content.FileProvider
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.app.Exports
import br.com.docmatch.contracts.Limits
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Decision
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Evidence
import br.com.docmatch.core.Finding
import br.com.docmatch.core.Format
import br.com.docmatch.core.Run
import br.com.docmatch.core.RunStatus
import br.com.docmatch.core.Step
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.UUID

sealed interface RunState {
    data object Loading : RunState

    data class Running(val step: Step?) : RunState

    /** Parked until the user approves the extra estimate (pending_quote). */
    data class NeedsApproval(val run: Run, val busy: Boolean = false) : RunState

    data class Done(val run: Run, val findings: List<Finding>) : RunState

    data class Failed(val message: String) : RunState
}

class RunViewModel(private val dm: Docmatch, private val runId: String) : ViewModel() {
    private val _state = MutableStateFlow<RunState>(RunState.Loading)
    val state: StateFlow<RunState> = _state
    private val _notice = MutableStateFlow<String?>(null)
    val notice: StateFlow<String?> = _notice
    private val _attempt = MutableStateFlow(0)

    /** Bumped to follow the run again (retry, after an approval); the screen keys its effect on it. */
    val attempt: StateFlow<Int> = _attempt
    private var approvalKey: Pair<String, String>? = null

    /**
     * Follows the run while the screen is shown: the screen's effect calls this and cancels it on
     * leaving, so a run left open in another tab does not poll in the background.
     */
    suspend fun follow() {
        if (_state.value is RunState.Done) return
        if (_state.value !is RunState.Running) _state.value = RunState.Loading
        _state.value =
            try {
                val run = dm.reconciler.follow(runId) { _state.value = RunState.Running(it) }
                when (run.status) {
                    RunStatus.COMPLETED -> RunState.Done(run, dm.api.findings(run.id).sortedWith(compareBy<Finding> { findingPriority(it.severity) }.thenByDescending { it.amountAtRisk }.thenBy { it.id }))
                    RunStatus.NEEDS_APPROVAL -> RunState.NeedsApproval(run)
                    else -> RunState.Failed(dm.texts.t("results.live_failed", mapOf("message" to (run.error?.message ?: dm.texts.t("errors.unknown")))))
                }
            } catch (e: ApiException) {
                RunState.Failed(e.message)
            }
    }

    fun retry() {
        _state.value = RunState.Loading
        _attempt.update { it + 1 }
    }

    /** Approves the extra estimate; the same key on a retry approves it once. */
    fun approve() {
        val parked = _state.value as? RunState.NeedsApproval ?: return
        val quote = parked.run.pendingQuote ?: return
        val key = approvalKey?.takeIf { it.first == quote.id }?.second ?: UUID.randomUUID().toString().also { approvalKey = quote.id to it }
        _state.value = parked.copy(busy = true)
        viewModelScope.launch {
            try {
                dm.api.approveQuote(runId, quote, key)
                _state.value = RunState.Running(null)
                _attempt.update { it + 1 }
            } catch (e: ApiException) {
                _state.value = parked.copy(busy = false)
                _notice.value = e.message
            }
        }
    }

    /** Records the decision on the API (the site and the other devices see it) and here. */
    fun decide(finding: Finding, decision: String, note: String? = null) {
        if (_state.value !is RunState.Done) return
        _notice.value = null
        viewModelScope.launch {
            try {
                dm.api.feedback(runId, finding.id, decision, note)
                _state.update { s ->
                    if (s is RunState.Done) s.copy(findings = s.findings.map { if (it.id == finding.id) it.copy(status = decision) else it }) else s
                }
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
                        Exports.dir(context).apply { mkdirs() }.resolve("docmatch-$runId.$format").apply { writeBytes(bytes) }
                    }
                val uri = FileProvider.getUriForFile(context, "${context.packageName}.exports", file)
                val mime = when (format) { "pdf" -> "application/pdf"; "csv" -> "text/csv"; else -> "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }
                val send = Intent(Intent.ACTION_SEND).setType(mime).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                context.startActivity(Intent.createChooser(send, dm.texts.t("mobile.export")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _notice.value = dm.texts.t("mobile.export_failed", mapOf("message" to (e.message ?: dm.texts.t("errors.unknown"))))
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RunScreen(vm: RunViewModel, onNewRun: () -> Unit) {
    val state by vm.state.collectAsState()
    val notice by vm.notice.collectAsState()
    val attempt by vm.attempt.collectAsState()
    var open by rememberSaveable { mutableStateOf<String?>(null) }
    val context = LocalContext.current
    val texts = LocalTexts.current

    // Polls only while this screen is in composition (and again on retry or after an approval).
    LaunchedEffect(attempt) { vm.follow() }

    when (val s = state) {
        RunState.Loading -> Loading(t("results.live_loading"))
        is RunState.Running ->
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(20.dp)) {
                ProcessingScene(s.step?.let { stepLabel(it) } ?: t("mobile.run_waiting"))
                Text(t("mobile.processing_keep_open"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        is RunState.NeedsApproval ->
            Column(Modifier.fillMaxSize().padding(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                Text(t("mobile.approve_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
                s.run.pendingQuote?.let { q ->
                    Text(t("mobile.approve_text", mapOf("expected" to q.expectedCredits, "cap" to q.capCredits)))
                    DmPrimaryButton(onClick = vm::approve, enabled = !s.busy, modifier = Modifier.fillMaxWidth()) { Text(t("mobile.approve")) }
                } ?: Text(t("mobile.status_needs_approval"))
                notice?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Assertive }) }
            }
        is RunState.Failed -> ErrorState(s.message, vm::retry)
        is RunState.Done -> {
            val names = s.run.sources.associate { it.id to it.fileName }
            val document = s.run.summary?.reconciliation
            val pending = s.findings.filter { it.status == "open" && it.severity != "info" }
            val critical = s.findings.filter { it.severity == "critical" }
            LazyColumn(modifier = Modifier.testTag("run-results"), contentPadding = PaddingValues(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                item {
                  DmCard(Modifier.fillMaxWidth()) {
                   Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    DmIconTile(DmIcons.Check)
                    Text(t(if (document != null) "results.document_ready" else "results.report_ready"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
                    Text(t(if (document != null) "results.output_description" else "results.report_subtitle"))
                    if (document != null) {
                        DmPrimaryButton(onClick = { vm.export(context, "xlsx") }, modifier = Modifier.fillMaxWidth()) { Text(t("results.download_corrected")) }
                        Text(t("results.doc_open") + ": " + Format.money(document.totals.openBalance), fontWeight = FontWeight.SemiBold)
                    } else {
                        Text(t("results.legacy_output"))
                        DmPrimaryButton(onClick = onNewRun, modifier = Modifier.fillMaxWidth()) { Text(t("results.generate_corrected")) }
                    }
                    DmSecondaryButton(onClick = { vm.export(context, "pdf") }, modifier = Modifier.fillMaxWidth()) { Text(t("results.download_report")) }
                    ResultsDisclosure(t("results.other_formats")) {
                        TextButton(onClick = { vm.export(context, "csv") }) { Text(t(if (document != null) "results.download_data_csv" else "results.download_findings_csv")) }
                        if (document == null) TextButton(onClick = { vm.export(context, "xlsx") }) { Text(t("results.download_findings_xlsx")) }
                    }
                    s.run.summary?.sourceWarnings?.takeIf { it.isNotEmpty() }?.let { warnings ->
                        Text(t("results.source_warnings_title"), fontWeight = FontWeight.SemiBold)
                        warnings.forEach { Text("• $it", style = MaterialTheme.typography.bodySmall) }
                    }
                    Text(t("mobile.run_findings", mapOf("count" to s.findings.size)))
                    if (open == null) notice?.let { Text(it, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
                }
                }
                }
                if (critical.isNotEmpty()) item {
                    Text(t("results.critical_heading"), fontWeight = FontWeight.SemiBold)
                    Text(t("results.critical_scope"))
                    critical.forEach { f -> FindingCard(f) { open = f.id } }
                }
                item { ResultsDisclosure(t("results.review_heading") + " · " + pending.size) {
                    Text(t("results.review_scope"))
                    if (pending.isEmpty()) Text(t("results.review_empty"))
                    pending.forEach { f -> FindingCard(f) { open = f.id } }
                    document?.receivables?.filter { it.proposedAmount > 0 || it.withholdingPending > 0 }?.forEach { DocumentRow(it) }
                } }
                if (document != null) item { ResultsDisclosure(t("results.changes_heading")) {
                    Text(t("results.doc_scope"))
                    Text(t("results.doc_principal") + ": " + Format.money(document.totals.principalConfirmed))
                    Text(t("results.doc_duplicate") + ": " + Format.money(document.totals.duplicateExcluded))
                    document.receivables.filter { it.appliedPrincipal > 0 || it.status == "duplicate" }.forEach { DocumentRow(it) }
                } }
                item { ResultsDisclosure(t("results.all_heading")) {
                    Text(t("results.findings_scope"))
                    document?.let { d ->
                        Text(t("results.doc_ledger"), fontWeight = FontWeight.SemiBold)
                        d.receivables.forEach { DocumentRow(it) }
                        Text(t("results.doc_bank"), fontWeight = FontWeight.SemiBold)
                        d.bankEntries.forEach { b ->
                            Text(b.description + " · " + Format.money(b.amount))
                            Text(b.sourceName + " · " + b.row, style = MaterialTheme.typography.bodySmall)
                            if (b.proposed) Text(t("results.doc_review"))
                            HorizontalDivider()
                        }
                    }
                    s.findings.forEach { f -> FindingCard(f) { open = f.id } }
                } }
            }
            s.findings.firstOrNull { it.id == open }?.let { current ->
                ModalBottomSheet(onDismissRequest = { open = null }) {
                    FindingDetail(current, document?.receivables?.filter { row -> current.evidence.any { it.sourceId == row.sourceId && it.locator?.row == row.sourceRow } }.orEmpty(), names, notice, texts.t("results.confidence_label", mapOf("level" to confidenceLabel(current))), onDecide = { d, note -> vm.decide(current, d, note) }) {
                        open = null
                    }
                }
            }
        }
    }
}

@Composable
private fun FindingCard(f: Finding, onOpen: () -> Unit) {
    val level = confidenceLabel(f)
    val described = t("results.confidence_label", mapOf("level" to level))
    DmCard(Modifier.fillMaxWidth().testTag(TAG_FINDING).clickable(onClick = onOpen)) {
        Column(Modifier.padding(DmSize.space2), verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
            Text(f.title, fontWeight = FontWeight.SemiBold)
            Text(Format.money(f.amountAtRisk))
            Row(horizontalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                Text(
                    level,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.semantics { contentDescription = described },
                )
                Text(statusLabel(f.status), color = MaterialTheme.colorScheme.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

@Composable
private fun FindingDetail(
    f: Finding,
    documentRows: List<ReconciledReceivable>,
    names: Map<String, String>,
    notice: String?,
    confidence: String,
    onDecide: (String, String?) -> Unit,
    onClose: () -> Unit,
) {
    var editing by remember(f.id) { mutableStateOf(false) }
    var note by rememberSaveable(f.id) { mutableStateOf("") }
    Column(
        Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(DmSize.space3).navigationBarsPadding(),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        Text(f.title, style = MaterialTheme.typography.titleLarge, modifier = Modifier.semantics { heading() })
        Text("$confidence · ${statusLabel(f.status)}", color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(t("results.amount_at_risk") + ": " + Format.money(f.amountAtRisk), fontWeight = FontWeight.SemiBold)
        if (f.explanation.isNotBlank()) {
            Text(t("results.explanation"), fontWeight = FontWeight.SemiBold, modifier = Modifier.semantics { heading() })
            Text(f.explanation)
        }
        Text(t("results.review_scope"), color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(t("results.file_effect"), fontWeight = FontWeight.SemiBold)
        if (documentRows.isEmpty()) Text(t("results.file_effect_unknown")) else documentRows.forEach { DocumentRow(it) }
        Text(t("results.evidence_heading"), fontWeight = FontWeight.SemiBold, modifier = Modifier.semantics { heading() })
        f.evidence.groupBy { it.sourceId }.forEach { (source, items) ->
            Text(names[source] ?: source, style = MaterialTheme.typography.labelLarge)
            items.forEach { EvidenceRow(it) }
            HorizontalDivider()
        }
        if (editing) {
            OutlinedTextField(
                value = note,
                onValueChange = { v -> note = cut(v, Limits.Text.FINDING_FEEDBACK_NOTE) },
                label = { Text(t("results.edit_label")) },
                modifier = Modifier.fillMaxWidth(),
            )
            CharCounter(codePoints(note), Limits.Text.FINDING_FEEDBACK_NOTE)
            Row(horizontalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                DmPrimaryButton(
                    onClick = {
                        onDecide(Decision.RESOLVED, note.trim())
                        editing = false
                    },
                    enabled = note.isNotBlank(),
                ) { Text(t("results.edit_save")) }
                TextButton(onClick = { editing = false }) { Text(t("results.edit_cancel")) }
            }
        } else {
            // Every decision is recorded on the API, so changing it is changing it everywhere.
            Column(verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
                DmPrimaryButton(onClick = { onDecide(Decision.CONFIRMED, null) }, enabled = f.status != Decision.CONFIRMED) { Text(t("results.accept")) }
                DmSecondaryButton(onClick = { onDecide(Decision.DISMISSED, null) }, enabled = f.status != Decision.DISMISSED) { Text(t("results.reject")) }
                DmSecondaryButton(onClick = { editing = true }) { Text(t("results.edit")) }
            }
        }
        notice?.let { Text(it, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
        TextButton(onClick = onClose) { Text(t("common.close")) }
    }
}

/** A value cut at a limit in code points after NFC. */
private fun cut(value: String, max: Int): String {
    val nfc = java.text.Normalizer.normalize(value, java.text.Normalizer.Form.NFC)
    return if (nfc.codePointCount(0, nfc.length) <= max) value else nfc.substring(0, nfc.offsetByCodePoints(0, max))
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

/** finding.json `confidence_label` (Confirmado, Provável, Precisa de você) as the site's level. */
@Composable
fun confidenceLabel(f: Finding): String =
    when (f.confidenceLabel) {
        "Confirmado" -> t("results.confidence_high")
        "Provável" -> t("results.confidence_medium")
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

@Composable
private fun ResultsDisclosure(label: String, content: @Composable () -> Unit) {
    DmDisclosure(label) { content() }
}

@Composable
private fun DocumentRow(row: ReconciledReceivable) {
    DmCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(DmSize.space2), verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
            Text(row.reference + " · " + row.customer, fontWeight = FontWeight.SemiBold)
            Text(t("results.file_balance_change", mapOf("before" to Format.money(row.originalAmount), "after" to Format.money(row.openAmount))))
            Text(t("results.doc_source", mapOf("name" to row.sourceName, "row" to row.sourceRow)), style = MaterialTheme.typography.bodySmall)
            if (row.status == "duplicate") Text(t("results.doc_duplicate_status", mapOf("id" to row.duplicateOf)))
            if (row.proposedAmount > 0) Text(t("results.file_proposed_change", mapOf("amount" to Format.money(row.proposedAmount))))
            if (row.withholdingPending > 0) Text(t("results.doc_tax_note", mapOf("amount" to Format.money(row.withholdingPending))))
        }
    }
}
