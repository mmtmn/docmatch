package br.com.docmatch.app.ui

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.contracts.Limits
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.FileTypes
import br.com.docmatch.core.Format
import br.com.docmatch.core.LocalFile
import br.com.docmatch.core.Prepared
import br.com.docmatch.core.Step
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

sealed interface NewRunPhase {
    data object Idle : NewRunPhase

    data class Working(val step: Step) : NewRunPhase

    data class Confirm(val prepared: Prepared) : NewRunPhase
}

class NewRunViewModel(private val dm: Docmatch) : ViewModel() {
    private val _files = MutableStateFlow<List<LocalFile>>(emptyList())
    val files: StateFlow<List<LocalFile>> = _files
    private val _rules = MutableStateFlow("")
    val rules: StateFlow<String> = _rules
    private val _phase = MutableStateFlow<NewRunPhase>(NewRunPhase.Idle)
    val phase: StateFlow<NewRunPhase> = _phase
    private val _error = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = _error

    /** Adds what the picker returned, refusing what the site would refuse (same messages). */
    fun add(context: Context, uris: List<Uri>) {
        viewModelScope.launch {
            val problems = mutableListOf<String>()
            val picked =
                withContext(Dispatchers.IO) {
                    uris.mapNotNull { uri ->
                        val (name, size) = metadata(context, uri)
                        FileTypes.problem(dm.texts, name, size)?.let {
                            problems += it
                            return@mapNotNull null
                        }
                        val bytes = context.contentResolver.openInputStream(uri)?.use { it.readBytes() } ?: return@mapNotNull null
                        LocalFile(name, FileTypes.mimeFor(name)!!, bytes)
                    }
                }
            val current = _files.value
            val fresh = picked.filter { p -> current.none { it.name == p.name } }
            picked.filter { p -> current.any { it.name == p.name } }.forEach { problems += dm.texts.t("errors.file_duplicate", mapOf("name" to it.name)) }
            _files.value = current + fresh
            _error.value = problems.joinToString("\n").ifEmpty { null }
        }
    }

    fun remove(name: String) {
        _files.value = _files.value.filterNot { it.name == name }
    }

    fun setRules(value: String) {
        _rules.value = value.take(Limits.Text.RULES_TEXT)
    }

    /** Uploads, scans and asks for the estimate; the user then confirms the tokens. */
    fun submit() {
        if (_files.value.isEmpty() || _phase.value != NewRunPhase.Idle) return
        _error.value = null
        viewModelScope.launch {
            try {
                val prepared = dm.reconciler.prepare(_files.value, _rules.value) { _phase.value = NewRunPhase.Working(it) }
                _phase.value = NewRunPhase.Confirm(prepared)
            } catch (e: ApiException) {
                _phase.value = NewRunPhase.Idle
                _error.value = e.message
            }
        }
    }

    fun cancel() {
        _phase.value = NewRunPhase.Idle
        _error.value = dm.texts.t("app.cancelled")
    }

    fun confirm(onStarted: (String) -> Unit) {
        val prepared = (_phase.value as? NewRunPhase.Confirm)?.prepared ?: return
        viewModelScope.launch {
            try {
                val run = dm.reconciler.start(prepared) { _phase.value = NewRunPhase.Working(it) }
                _files.value = emptyList()
                _rules.value = ""
                _phase.value = NewRunPhase.Idle
                onStarted(run.id)
            } catch (e: ApiException) {
                _phase.value = NewRunPhase.Idle
                _error.value = e.message
            }
        }
    }

    private fun metadata(context: Context, uri: Uri): Pair<String, Long> {
        context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                val name = c.getString(0) ?: uri.lastPathSegment.orEmpty()
                val size = if (c.isNull(1)) -1L else c.getLong(1)
                return name to size
            }
        }
        return (uri.lastPathSegment ?: "arquivo") to -1L
    }
}

@Composable
fun stepLabel(step: Step): String =
    when (step) {
        is Step.Uploading -> t("app.uploading", mapOf("done" to step.done, "total" to step.total))
        is Step.Scanning -> t("app.scanning", mapOf("name" to step.name))
        Step.Estimating -> t("app.estimating")
        Step.Starting -> t("app.starting")
        is Step.Running -> step.message?.let { t("results.live_running", mapOf("message" to it)) } ?: t("mobile.status_running")
    }

@Composable
fun NewRunScreen(vm: NewRunViewModel, onStarted: (String) -> Unit) {
    val context = LocalContext.current
    val files by vm.files.collectAsState()
    val rules by vm.rules.collectAsState()
    val phase by vm.phase.collectAsState()
    val error by vm.error.collectAsState()
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris -> vm.add(context, uris) }
    val busy = phase !is NewRunPhase.Idle

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).imePadding().padding(DmSize.space3),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        Text(t("mobile.new_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
        Text(t("mobile.new_text"), color = MaterialTheme.colorScheme.onSurfaceVariant)
        OutlinedButton(onClick = { picker.launch(arrayOf("*/*")) }, enabled = !busy, modifier = Modifier.fillMaxWidth()) {
            Text(t("mobile.pick_files"))
        }
        if (files.isNotEmpty()) {
            Text(t("mobile.files_selected", mapOf("count" to files.size, "size" to Format.size(files.sumOf { it.bytes.size.toLong() }))))
            files.forEach { f ->
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("${f.name} · ${Format.size(f.bytes.size.toLong())}", modifier = Modifier.weight(1f))
                    IconButton(onClick = { vm.remove(f.name) }, enabled = !busy) {
                        Icon(Icons.Filled.Close, contentDescription = t("mobile.remove_file", mapOf("name" to f.name)))
                    }
                }
            }
        }
        OutlinedTextField(
            value = rules,
            onValueChange = vm::setRules,
            label = { Text(t("form.rules_label")) },
            placeholder = { Text(t("form.rules_placeholder")) },
            supportingText = { Text(t("form.rules_help")) },
            enabled = !busy,
            modifier = Modifier.fillMaxWidth().heightIn(min = DmSize.space16),
        )
        Button(onClick = vm::submit, enabled = !busy && files.isNotEmpty(), modifier = Modifier.fillMaxWidth()) {
            Text(if (busy) t("form.submitting") else t("form.submit"))
        }
        (phase as? NewRunPhase.Working)?.let { w ->
            LinearProgressIndicator(Modifier.fillMaxWidth())
            Text(stepLabel(w.step), modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        }
        error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Assertive }) }
    }

    (phase as? NewRunPhase.Confirm)?.let { c ->
        AlertDialog(
            onDismissRequest = vm::cancel,
            title = { Text(t("mobile.confirm_title")) },
            text = { Text(t("app.confirm_run", mapOf("expected" to c.prepared.quote.expectedCredits, "cap" to c.prepared.quote.capCredits))) },
            confirmButton = { Button(onClick = { vm.confirm(onStarted) }) { Text(t("mobile.confirm_continue")) } },
            dismissButton = { TextButton(onClick = vm::cancel) { Text(t("common.cancel")) } },
        )
    }
}
