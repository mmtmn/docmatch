package br.com.docmatch.app.ui

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.ui.unit.dp
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.window.Dialog
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.withFrameNanos
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.contracts.Limits
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Candidate
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Format
import br.com.docmatch.core.LocalFile
import br.com.docmatch.core.Precheck
import br.com.docmatch.core.Prepared
import br.com.docmatch.core.RunOptions
import br.com.docmatch.core.Step
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.IOException
import java.io.InputStream
import java.text.Normalizer
import kotlin.math.ceil

sealed interface NewRunPhase {
    data object Idle : NewRunPhase

    data class Working(val step: Step) : NewRunPhase

    data class Confirm(val prepared: Prepared) : NewRunPhase
}

class NewRunViewModel(private val dm: Docmatch) : ViewModel() {
    private val _files = MutableStateFlow<List<LocalFile>>(emptyList())
    val files: StateFlow<List<LocalFile>> = _files
    private val _options = MutableStateFlow(RunOptions())
    val options: StateFlow<RunOptions> = _options
    private val _phase = MutableStateFlow<NewRunPhase>(NewRunPhase.Idle)
    val phase: StateFlow<NewRunPhase> = _phase
    private val _error = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = _error
    private val _started = MutableStateFlow<String?>(null)

    /** The run just started, for the screen to open once (survives a rotation mid-request). */
    val started: StateFlow<String?> = _started

    /**
     * Adds what the picker returned through the site's precheck (kind from the content, caps,
     * duplicates, messages). Only the name, size and kind stay in memory; the bytes are streamed
     * from the Uri while uploading.
     */
    fun add(context: Context, uris: List<Uri>) {
        viewModelScope.launch {
            val resolver = context.contentResolver
            val (accepted, problems) =
                withContext(Dispatchers.IO) {
                    val sources = mutableMapOf<String, Uri>()
                    val candidates =
                        uris.map { uri ->
                            val (name, reported) = metadata(context, uri)
                            val size = if (reported >= 0) reported else runCatching { measure(resolver.openInputStream(uri)) }.getOrDefault(-1L)
                            sources["$name/$size"] = uri
                            Candidate(name, size) { head(resolver.openInputStream(uri)) }
                        }
                    val (ok, refused) = Precheck.check(dm.texts, candidates, _files.value)
                    ok.map { a ->
                        val uri = sources.getValue("${a.name}/${a.size}")
                        LocalFile(a.name, a.mime, a.size, a.kind) { resolver.openInputStream(uri) ?: throw IOException("sem conteúdo") }
                    } to refused
                }
            _files.value = _files.value + accepted
            _error.value = problems.joinToString("\n").ifEmpty { null }
        }
    }

    /** By name and size: two different files may share a name. */
    fun remove(file: LocalFile) {
        if (_phase.value != NewRunPhase.Idle) return
        _files.value = _files.value.filterNot { it.name == file.name && it.size == file.size }
    }

    /** The rules text, cut at the API's limit (code points after NFC, like the site counts). */
    fun setRules(value: String) {
        val nfc = Normalizer.normalize(value, Normalizer.Form.NFC)
        val max = Limits.Text.RULES_TEXT
        val text = if (nfc.codePointCount(0, nfc.length) <= max) value else nfc.substring(0, nfc.offsetByCodePoints(0, max))
        _options.value = _options.value.copy(rulesText = text)
    }

    fun setUseRules(on: Boolean) {
        _options.value = _options.value.copy(useRules = on)
    }

    fun setWebSearch(on: Boolean) {
        _options.value = _options.value.copy(webSearch = on)
    }

    /** Uploads, scans and asks for the estimate; the user then confirms the tokens. */
    fun submit() {
        if (_files.value.isEmpty() || _phase.value != NewRunPhase.Idle) return
        _error.value = null
        _phase.value = NewRunPhase.Working(Step.Uploading(0, _files.value.size))
        viewModelScope.launch {
            try {
                val prepared = dm.reconciler.prepare(_files.value, _options.value) { _phase.value = NewRunPhase.Working(it) }
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

    /**
     * Starts the accepted run. A lost answer or a server error keeps the confirmation open: the
     * next tap sends the same estimate with the same key, so it can never start a second run.
     */
    fun confirm() {
        val prepared = (_phase.value as? NewRunPhase.Confirm)?.prepared ?: return
        _phase.value = NewRunPhase.Working(Step.Starting)
        viewModelScope.launch {
            try {
                val run = dm.reconciler.start(prepared)
                _files.value = emptyList()
                _options.value = RunOptions()
                _phase.value = NewRunPhase.Idle
                _error.value = null
                _started.value = run.id
            } catch (e: ApiException) {
                val retryable = e.status == 0 || e.status >= 500
                _phase.value = if (retryable) NewRunPhase.Confirm(prepared) else NewRunPhase.Idle
                _error.value = e.message
            }
        }
    }

    fun startedShown() {
        _started.value = null
    }

    private fun metadata(context: Context, uri: Uri): Pair<String, Long> {
        runCatching {
            context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
                if (c.moveToFirst()) {
                    val name = c.getString(0) ?: uri.lastPathSegment.orEmpty()
                    val size = if (c.isNull(1)) -1L else c.getLong(1)
                    return name to size
                }
            }
        }
        return (uri.lastPathSegment ?: "arquivo") to -1L
    }

    private companion object {
        /** The first bytes, for the content check (readNBytes is API 33+). */
        fun head(input: InputStream?): ByteArray {
            val stream = input ?: throw IOException("sem conteúdo")
            stream.use {
                val out = ByteArray(Precheck.HEAD_BYTES)
                var n = 0
                while (n < out.size) {
                    val r = it.read(out, n, out.size - n)
                    if (r < 0) break
                    n += r
                }
                return out.copyOf(n)
            }
        }

        /** The real size when the provider does not say, counted up to just past the largest cap. */
        fun measure(input: InputStream?): Long {
            val stream = input ?: throw IOException("sem conteúdo")
            val cap = Precheck.mediaFileBytes + 1
            stream.use {
                val buffer = ByteArray(64 * 1024)
                var total = 0L
                while (total < cap) {
                    val r = it.read(buffer)
                    if (r < 0) break
                    total += r
                }
                return total
            }
        }
    }
}

@Composable
fun stepLabel(step: Step): String =
    when (step) {
        is Step.Uploading -> t("app.uploading", mapOf("done" to maxOf(step.done, 1), "total" to step.total))
        is Step.Scanning -> t("app.scanning", mapOf("name" to step.name))
        Step.Estimating -> t("app.estimating")
        Step.Starting -> t("app.starting")
        is Step.Running -> step.message?.let { t("results.live_running", mapOf("message" to it)) } ?: t("mobile.status_running")
    }

/** The site's counter: shown from 80% of the limit, announced as it changes. */
@Composable
fun CharCounter(count: Int, max: Int) {
    if (count < ceil(max * 0.8).toInt()) return
    val text = if (count >= max) t("form.counter_limit", mapOf("max" to max)) else t("form.counter", mapOf("count" to count, "max" to max))
    Text(
        text,
        style = MaterialTheme.typography.bodySmall,
        color = if (count >= max) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
    )
}

/** Code points after NFC, how limits.yaml counts. */
fun codePoints(s: String): Int = Normalizer.normalize(s, Normalizer.Form.NFC).let { it.codePointCount(0, it.length) }

@Composable
fun NewRunScreen(vm: NewRunViewModel, tokenExempt: Boolean = false, onStarted: (String) -> Unit) {
    val context = LocalContext.current
    val texts = LocalTexts.current
    val files by vm.files.collectAsState()
    val options by vm.options.collectAsState()
    val phase by vm.phase.collectAsState()
    val error by vm.error.collectAsState()
    val started by vm.started.collectAsState()
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris -> vm.add(context, uris) }
    val busy = phase !is NewRunPhase.Idle
    val scroll = rememberScrollState()
    LaunchedEffect(error) {
        if (error != null) {
            withFrameNanos { }
            scroll.animateScrollTo(scroll.maxValue)
        }
    }

    LaunchedEffect(started) {
        started?.let {
            vm.startedShown()
            onStarted(it)
        }
    }

    Column(Modifier.fillMaxSize().imePadding().padding(horizontal = 16.dp)) {
        Column(Modifier.weight(1f).verticalScroll(scroll).padding(top = 20.dp, bottom = 16.dp), verticalArrangement = Arrangement.spacedBy(20.dp)) {
            if (phase is NewRunPhase.Working) {
                ProcessingScene(stepLabel((phase as NewRunPhase.Working).step))
            } else {
                DmHeading(t("landing.title"), t("landing.subtitle"), hero = true)
                UploadTarget(!busy, files.isNotEmpty() || options.rulesText.length > 120) { picker.launch(arrayOf("*/*")) }
                if (files.isNotEmpty()) {
                    DmCard(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            val count = if (files.size == 1) t("dropzone.files_count_one") else t("dropzone.files_count_many", mapOf("count" to files.size))
                            Text("$count · " + Format.size(texts, files.sumOf { it.size }), style = MaterialTheme.typography.labelLarge)
                            Column(Modifier.heightIn(max = 240.dp).verticalScroll(rememberScrollState())) {
                                files.forEach { f ->
                                    Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                                        Icon(DmIcons.File, null, tint = MaterialTheme.colorScheme.primary)
                                        Spacer(Modifier.width(10.dp))
                                        Column(Modifier.weight(1f)) {
                                            Text(f.name, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                            Text(kindLabel(f.kind.id) + " · " + Format.size(texts, f.size), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                        }
                                        IconButton(onClick = { vm.remove(f) }, enabled = !busy) {
                                            Icon(Icons.Filled.Close, contentDescription = t("dropzone.remove_file", mapOf("name" to f.name)))
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(t("form.rules_label"), style = MaterialTheme.typography.titleSmall, modifier = Modifier.semantics { heading() })
                    OptionSwitch(t("form.use_rules"), t("tooltips.use_rules"), options.useRules, !busy, vm::setUseRules)
                    OptionSwitch(t("form.web_search"), t("tooltips.web_search"), options.webSearch, !busy, vm::setWebSearch)
                    OutlinedTextField(value = options.rulesText, onValueChange = vm::setRules,
                        placeholder = { Text(t("form.rules_placeholder")) }, enabled = !busy && options.useRules,
                        minLines = 2, maxLines = 6, shape = RoundedCornerShape(DmSize.radiusMd),
                        modifier = Modifier.fillMaxWidth().animateContentSize().semantics { contentDescription = texts.t("form.rules_label") })
                    CharCounter(codePoints(options.rulesText), Limits.Text.RULES_TEXT)
                    if (!options.useRules) Text(t("form.rules_disabled_hint"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    DmDisclosure(t("mobile.rules_more")) {
                        Text(t("tooltips.use_rules"), style = MaterialTheme.typography.bodySmall)
                        Text(t("tooltips.web_search"), style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        }
        Column(Modifier.padding(vertical = 12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            error?.let { DmNotice(it, error = true) }
            DmPrimaryButton(onClick = vm::submit, enabled = !busy && files.isNotEmpty(), modifier = Modifier.fillMaxWidth().testTag("submit-run")) {
                Text(if (busy) t("form.submitting") else t("form.submit"))
            }
            Text(t(if (files.isEmpty()) "form.disabled_no_files" else "mobile.files_private"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }

    (phase as? NewRunPhase.Confirm)?.let { c ->
        Dialog(onDismissRequest = vm::cancel) {
            DmCard(Modifier.fillMaxWidth()) {
                Column(Modifier.heightIn(max = 600.dp).verticalScroll(rememberScrollState()).padding(24.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                    DmIconTile(DmIcons.File)
                    DmHeading(t("mobile.confirm_title"), t("mobile.review_before_start"))
                    Text(if (tokenExempt) t("app.confirm_exempt") else t("app.confirm_run", mapOf("expected" to c.prepared.quote.expectedCredits, "cap" to c.prepared.quote.capCredits)))
                    Text(t("app.confirm_scope"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Format.largeNotice(texts, c.prepared.quote)?.let { DmNotice(it) }
                    error?.let { DmNotice(it, error = true) }
                    DmPrimaryButton(onClick = vm::confirm, modifier = Modifier.fillMaxWidth()) { Text(t("form.submit")) }
                    DmSecondaryButton(onClick = vm::cancel, modifier = Modifier.fillMaxWidth()) { Text(t("common.cancel")) }
                }
            }
        }
    }
}

@Composable
private fun OptionSwitch(label: String, help: String, checked: Boolean, enabled: Boolean, onChange: (Boolean) -> Unit) {
    // The whole row is the switch, so TalkBack reads the label and the help with its state.
    Row(
        Modifier.fillMaxWidth().toggleable(value = checked, enabled = enabled, role = Role.Switch, onValueChange = onChange).semantics { contentDescription = "$label. $help" },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(label)

        }
        Switch(checked = checked, onCheckedChange = null, enabled = enabled)
    }
}

/** The site's label for a kind (dropzone.kind_*). */
@Composable
fun kindLabel(kind: String): String = t("dropzone.kind_$kind")
