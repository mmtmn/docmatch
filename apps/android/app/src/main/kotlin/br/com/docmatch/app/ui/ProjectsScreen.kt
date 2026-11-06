package br.com.docmatch.app.ui

import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class ProjectsState(
    val items: List<SavedProject> = emptyList(),
    val next: String? = null,
    val archived: Boolean = false,
    val busy: Boolean = false,
    val error: String? = null,
    val created: String? = null,
)

class ProjectsViewModel(private val dm: Docmatch) : ViewModel() {
    private val _state = MutableStateFlow(ProjectsState())
    val state: StateFlow<ProjectsState> = _state

    fun load(more: Boolean = false, archived: Boolean = state.value.archived) {
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null, archived = archived) }
        viewModelScope.launch {
            try {
                val page =
                    dm.api.projects(
                        cursor = if (more) state.value.next else null,
                        archived = archived,
                    )
                _state.update {
                    it.copy(
                        items =
                            if (more) (it.items + page.items).distinctBy { p -> p.id }
                            else page.items,
                        next = page.nextCursor,
                    )
                }
            } catch (e: ApiException) {
                _state.update {
                    it.copy(
                        items = if (e.status in listOf(401, 403)) emptyList() else it.items,
                        error = e.message,
                    )
                }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun create(name: String) {
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val project = dm.api.createProject(name)
                _state.update { it.copy(created = project.id) }
            } catch (e: ApiException) {
                _state.update { it.copy(error = e.message) }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun opened() {
        _state.update { it.copy(created = null) }
    }
}

@Composable
fun ProjectsScreen(vm: ProjectsViewModel, account: AccountMembership, onOpen: (String) -> Unit) {
    val s by vm.state.collectAsState()
    var name by rememberSaveable { mutableStateOf("") }
    LaunchedEffect(Unit) { vm.load() }
    LaunchedEffect(s.created) {
        s.created?.let {
            vm.opened()
            name = ""
            onOpen(it)
        }
    }
    LazyColumn(
        contentPadding = PaddingValues(DmSize.space3),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        item { DmHeading(t("projects.title"), t("projects.intro")) }
        if (account.capabilities.createProject)
            item {
                DmCard {
                    Column(
                        Modifier.padding(DmSize.space2),
                        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
                    ) {
                        OutlinedTextField(
                            name,
                            { name = it },
                            label = { Text(t("projects.name")) },
                            placeholder = { Text(t("projects.name_example")) },
                            modifier = Modifier.fillMaxWidth().testTag("project-name"),
                            singleLine = true,
                            enabled = !s.busy,
                        )
                        DmPrimaryButton(
                            { vm.create(name) },
                            enabled = name.isNotBlank() && !s.busy,
                        ) {
                            Text(t("projects.new"))
                        }
                    }
                }
            }
        item {
            ProjectToggle(t("projects.show_archived"), s.archived, !s.busy) {
                vm.load(archived = it)
            }
        }
        if (s.items.isEmpty() && !s.busy)
            item {
                Text(
                    t(
                        if (account.capabilities.createProject) "projects.empty_body"
                        else "projects.empty_upload_only",
                    ),
                )
            }
        items(s.items, key = { it.id }) { project ->
            DmCard(Modifier.fillMaxWidth().clickable { onOpen(project.id) }) {
                Column(
                    Modifier.padding(DmSize.space2),
                    verticalArrangement = Arrangement.spacedBy(DmSize.space1),
                ) {
                    Text(project.name, style = MaterialTheme.typography.titleMedium)
                    Text(t("projects.documents_count", mapOf("count" to project.documentCount)))
                    Text(
                        t("projects.updated", mapOf("date" to Format.dateTime(project.updatedAt))),
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            }
        }
        s.error?.let {
            item {
                DmNotice(it, error = true)
                TextButton({ vm.load() }) { Text(t("projects.retry_load")) }
            }
        }
        if (s.busy) item { CircularProgressIndicator() }
        if (s.next != null)
            item {
                TextButton({ vm.load(more = true) }, enabled = !s.busy) { Text(t("projects.more")) }
            }
    }
}

@Composable
fun ProjectScreen(
    vm: ProjectViewModel,
    accountName: String,
    session: SessionViewModel,
    onBack: () -> Unit,
    onRun: (String) -> Unit,
) {
    val s by vm.state.collectAsState()
    val context = LocalContext.current
    var section by rememberSaveable { mutableStateOf("documents") }
    var showVersions by rememberSaveable { mutableStateOf(false) }
    var filesExpanded by rememberSaveable { mutableStateOf(false) }
    val activeDocuments =
        s.project?.documents.orEmpty().filter {
            it.removedAt == null && it.pendingReplacementId == null
        }
    var replacing by remember { mutableStateOf<ProjectDocument?>(null) }
    var removing by remember { mutableStateOf<ProjectDocument?>(null) }
    var leaving by remember { mutableStateOf(false) }
    var reload by remember { mutableStateOf(false) }
    var management by remember { mutableStateOf<String?>(null) }
    var reason by rememberSaveable { mutableStateOf("") }
    val picker =
        rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
            if (uris.isNotEmpty()) vm.add(context, uris, replacing?.id)
            replacing = null
        }
    val back = {
        if (!s.busy && s.save != ProjectSaveState.SAVING) {
            if (s.dirty) leaving = true else onBack()
        }
    }
    BackHandler { back() }
    OnResume(vm::validateAccess)
    LaunchedEffect(s.dirty, s.busy, s.save) {
        session.projectWork(s.dirty, s.busy || s.save == ProjectSaveState.SAVING)
    }
    DisposableEffect(Unit) { onDispose { session.projectWork(false, false) } }
    LaunchedEffect(s.openedRun) {
        s.openedRun?.let {
            vm.openedRunShown()
            onRun(it)
        }
    }
    LaunchedEffect(s.deleted) { if (s.deleted) onBack() }
    management?.let { action ->
        ProjectConfirmation(
            t("projects.${action}_confirm", mapOf("name" to s.name)),
            { management = null },
        ) {
            management = null
            if (action == "delete") vm.delete() else vm.archive(action == "archive")
        }
    }
    if (leaving)
        ProjectConfirmation(t("projects.leave_unsaved"), { leaving = false }) {
            vm.discardPendingEdits()
            leaving = false
            onBack()
        }
    if (reload)
        ProjectConfirmation(t("projects.conflict_help"), { reload = false }) {
            reload = false
            vm.load(discardEdits = true)
        }
    removing?.let { doc ->
        ProjectConfirmation(
            t("projects.remove_document_confirm", mapOf("name" to doc.fileName)),
            { removing = null },
        ) {
            removing = null
            vm.remove(doc)
        }
    }
    s.analysis?.quote?.let { quote ->
        AlertDialog(
            onDismissRequest = { if (!s.busy) vm.dismissApproval() },
            title = { Text(t("mobile.confirm_title")) },
            text = {
                Column(
                    Modifier.verticalScroll(rememberScrollState()),
                    verticalArrangement = Arrangement.spacedBy(DmSize.space2),
                ) {
                    Text(
                        t(
                            "projects.confirm_context",
                            mapOf("account" to accountName, "project" to s.name),
                        ),
                    )
                    Text(
                        t(
                            "app.confirm_run",
                            mapOf("expected" to quote.expectedCredits, "cap" to quote.capCredits),
                        ),
                    )
                    s.error?.let { DmNotice(it, true) }
                }
            },
            confirmButton = {
                TextButton(vm::approve, enabled = !s.busy) { Text(t("common.confirm")) }
            },
            dismissButton = {
                TextButton(vm::dismissApproval, enabled = !s.busy) { Text(t("common.cancel")) }
            },
        )
    }
    Column(Modifier.fillMaxSize().imePadding()) {
        LazyColumn(
            modifier = Modifier.weight(1f).testTag("project-scroll"),
            contentPadding = PaddingValues(DmSize.space3),
            verticalArrangement = Arrangement.spacedBy(DmSize.space2),
        ) {
            item {
                TextButton(back, enabled = !s.busy && s.save != ProjectSaveState.SAVING) {
                    Text(t("projects.back"))
                }
            }
            s.project?.let { project ->
                item {
                    Text(accountName, style = MaterialTheme.typography.labelLarge)
                    OutlinedTextField(
                        s.name,
                        vm::rename,
                        label = { Text(t("projects.name")) },
                        enabled = s.canEdit && !s.busy,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Text(
                        t(
                            when (s.save) {
                                ProjectSaveState.SAVED -> "projects.saved"
                                ProjectSaveState.SAVING -> "projects.saving"
                                ProjectSaveState.CONFLICT -> "projects.conflict"
                                else -> "projects.unsaved"
                            },
                        ),
                        modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
                    )
                    if (s.save == ProjectSaveState.CONFLICT) {
                        DmNotice(t("projects.conflict_help"), true)
                        TextButton({ reload = true }) { Text(t("projects.reload")) }
                    }
                    if (s.save == ProjectSaveState.OFFLINE)
                        TextButton(vm::retrySave) { Text(t("projects.retry_save")) }
                    if (!project.capabilities.readResults) DmNotice(t("projects.upload_permission"))
                }
                item {
                    val tabs =
                        listOf("documents") +
                            (if (project.capabilities.readResults) listOf("results")
                            else emptyList()) +
                            "activity"
                    TabRow(selectedTabIndex = tabs.indexOf(section).coerceAtLeast(0)) {
                        tabs.forEach { key ->
                            Tab(
                                selected = section == key,
                                onClick = { section = key },
                                text = { Text(t("projects.$key")) },
                            )
                        }
                    }
                }
                when (section) {
                    "activity" ->
                        items(s.events, key = { it.id }) { event ->
                            Text(
                                projectEvent(event.action) +
                                    " · " +
                                    Format.dateTime(event.createdAt),
                            )
                        }
                    "results" -> {
                        if (s.nextRuns != null)
                            item {
                                TextButton(vm::moreRuns, enabled = !s.busy) {
                                    Text(t("projects.more"))
                                }
                            }
                        if (s.runs.isEmpty()) item { Text(t("projects.no_results")) }
                        items(s.runs, key = { it.id }) { run ->
                            TextButton({ onRun(run.id) }) {
                                Text(runStatus(run.status) + " · " + Format.dateTime(run.createdAt))
                            }
                        }
                    }
                    else -> {
                        if (project.capabilities.upload && project.archivedAt == null)
                            item {
                                DmPrimaryButton(
                                    {
                                        replacing = null
                                        picker.launch(arrayOf("*/*"))
                                    },
                                    enabled = !s.busy,
                                ) {
                                    Text(t("projects.add_documents"))
                                }
                                Text(
                                    t(
                                        "projects.upload_destination",
                                        mapOf("account" to accountName, "project" to project.name),
                                    ),
                                    style = MaterialTheme.typography.bodySmall,
                                )
                            }
                        if (project.documents.orEmpty().isNotEmpty())
                            item {
                                DmCard {
                                    Column(Modifier.padding(DmSize.space2)) {
                                        Text(
                                            t(
                                                "projects.files_ready",
                                                mapOf(
                                                    "ready" to
                                                        activeDocuments.count {
                                                            it.status == "ready"
                                                        },
                                                    "total" to activeDocuments.size,
                                                ),
                                            ),
                                            modifier =
                                                Modifier.semantics {
                                                    liveRegion = LiveRegionMode.Polite
                                                },
                                        )
                                        if (
                                            project.documents.orEmpty().any {
                                                it.removedAt == null &&
                                                    (it.status in
                                                        listOf("rejected", "expired", "deleted") ||
                                                        it.pendingReplacementId != null)
                                            }
                                        )
                                            DmNotice(t("projects.files_attention"), true)
                                        val disclosureState =
                                            t(
                                                if (filesExpanded) "common.expanded"
                                                else "common.collapsed",
                                            )
                                        TextButton(
                                            { filesExpanded = !filesExpanded },
                                            modifier =
                                                Modifier.testTag("project-files-disclosure")
                                                    .semantics {
                                                        stateDescription = disclosureState
                                                    },
                                        ) {
                                            Text(
                                                if (filesExpanded) t("projects.hide_files")
                                                else
                                                    t(
                                                        "projects.show_files",
                                                        mapOf("count" to activeDocuments.size),
                                                    ),
                                            )
                                            Icon(
                                                if (filesExpanded) DmIcons.Up else DmIcons.Down,
                                                contentDescription = null,
                                            )
                                        }
                                    }
                                }
                            }
                        if (filesExpanded) {
                            items(
                                project.documents.orEmpty().filter {
                                    showVersions || it.removedAt == null
                                },
                                key = { it.id },
                            ) { doc ->
                                DmCard {
                                    Column(
                                        Modifier.padding(DmSize.space2),
                                        verticalArrangement = Arrangement.spacedBy(DmSize.space1),
                                    ) {
                                        Text(
                                            doc.fileName,
                                            style = MaterialTheme.typography.titleMedium,
                                        )
                                        Text(documentStatus(doc))
                                        doc.expiresAt?.let {
                                            Text(
                                                t(
                                                    "projects.expires",
                                                    mapOf("date" to Format.dateTime(it)),
                                                ),
                                                style = MaterialTheme.typography.bodySmall,
                                            )
                                        }
                                        if (s.canEdit && doc.removedAt == null) {
                                            if (doc.pendingReplacementId != null)
                                                TextButton(
                                                    { vm.applyReplacement(doc) },
                                                    enabled = !s.busy && doc.status == "ready",
                                                ) {
                                                    Text(t("projects.replacement_apply"))
                                                }
                                            else
                                                TextButton(
                                                    {
                                                        replacing = doc
                                                        picker.launch(arrayOf("*/*"))
                                                    },
                                                    enabled = !s.busy,
                                                ) {
                                                    Text(t("projects.replace_document"))
                                                }
                                            TextButton({ removing = doc }, enabled = !s.busy) {
                                                Text(
                                                    t("projects.remove_document"),
                                                    color = MaterialTheme.colorScheme.error,
                                                )
                                            }
                                        }
                                    }
                                }
                            }
                            if (project.documents.orEmpty().any { it.removedAt != null })
                                item {
                                    ProjectToggle(t("projects.show_versions"), showVersions, true) {
                                        showVersions = it
                                    }
                                }
                        }
                        item {
                            Text(
                                t("projects.retention"),
                                style = MaterialTheme.typography.bodySmall,
                            )
                        }
                        if (s.canEdit)
                            item {
                                DmDisclosure(t("projects.settings")) {
                                    DmCard {
                                        Column(
                                            Modifier.padding(DmSize.space2),
                                            verticalArrangement =
                                                Arrangement.spacedBy(DmSize.space2),
                                        ) {
                                            ProjectToggle(
                                                t("projects.use_rules"),
                                                s.settings.useRules,
                                                !s.busy,
                                            ) {
                                                vm.settings(s.settings.copy(useRules = it))
                                            }
                                            ProjectToggle(
                                                t("projects.web_search"),
                                                s.settings.webSearch,
                                                !s.busy,
                                            ) {
                                                vm.settings(s.settings.copy(webSearch = it))
                                            }
                                            OutlinedTextField(
                                                s.settings.rulesText,
                                                { vm.settings(s.settings.copy(rulesText = it)) },
                                                label = { Text(t("projects.rules")) },
                                                minLines = 3,
                                                modifier = Modifier.fillMaxWidth(),
                                                enabled = !s.busy,
                                            )
                                        }
                                    }
                                    ProjectRulesSection(vm)
                                }
                                if (s.analysis?.state == "expired") {
                                    DmNotice(t("projects.expired_result"))
                                    OutlinedTextField(
                                        reason,
                                        { reason = it },
                                        label = { Text(t("projects.reprocess_reason")) },
                                        modifier = Modifier.fillMaxWidth(),
                                    )
                                }
                            }
                    }
                }
            }
            if (s.project?.capabilities?.manageAccount == true) item { ProjectSharingSection(vm) }
            if (s.project?.capabilities?.reconcile == true)
                item {
                    DmDisclosure(t("projects.manage")) {
                        val action = if (s.project?.archivedAt == null) "archive" else "restore"
                        TextButton({ management = action }, enabled = !s.busy) {
                            Text(t("projects.$action"))
                        }
                        TextButton({ management = "delete" }, enabled = !s.busy) {
                            Text(t("projects.delete"), color = MaterialTheme.colorScheme.error)
                        }
                    }
                }
            s.error?.let { item { DmNotice(it, true) } }
            if (s.project == null && !s.busy)
                item { TextButton({ vm.load() }) { Text(t("projects.retry_load")) } }
            if (s.busy) item { CircularProgressIndicator() }
        }
        if (section == "documents" && s.canEdit) {
            HorizontalDivider()
            Column(
                Modifier.fillMaxWidth()
                    .padding(horizontal = DmSize.space3, vertical = DmSize.space2),
                verticalArrangement = Arrangement.spacedBy(DmSize.space1),
            ) {
                Text(t("projects.no_duplicate_cost"), style = MaterialTheme.typography.bodySmall)
                DmPrimaryButton(
                    { vm.prepare(reason) },
                    modifier = Modifier.testTag("submit-run"),
                    enabled =
                        !s.busy &&
                            s.save != ProjectSaveState.SAVING &&
                            s.save != ProjectSaveState.CONFLICT &&
                            activeDocuments.isNotEmpty(),
                ) {
                    Text(t("projects.reconcile"))
                }
            }
        }
    }
}

@Composable
internal fun ProjectToggle(
    label: String,
    checked: Boolean,
    enabled: Boolean,
    onChange: (Boolean) -> Unit,
) {
    Row(
        Modifier.fillMaxWidth(),
        verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
    ) {
        Text(label, Modifier.weight(1f))
        Switch(checked, onChange, enabled = enabled)
    }
}

@Composable
internal fun ProjectConfirmation(message: String, onCancel: () -> Unit, onConfirm: () -> Unit) {
    AlertDialog(
        onDismissRequest = onCancel,
        text = { Text(message) },
        confirmButton = { TextButton(onConfirm) { Text(t("common.confirm")) } },
        dismissButton = { TextButton(onCancel) { Text(t("common.cancel")) } },
    )
}

@Composable
private fun documentStatus(doc: ProjectDocument): String =
    t(
        when {
            doc.removedAt != null -> "projects.document_removed"
            doc.pendingReplacementId != null -> "projects.replacement_pending"
            doc.status in listOf("ready", "scanning", "rejected", "expired") ->
                "projects.document_${doc.status}"
            doc.status == "deleted" -> "projects.document_expired"
            else -> "projects.document_pending"
        },
    )

@Composable
private fun projectEvent(action: String): String =
    t(
        "projects." +
            (mapOf(
                "project.created" to "event_created",
                "project.updated" to "event_updated",
                "project.archived" to "event_archived",
                "project.restored" to "event_restored",
                "project.sharing_changed" to "event_shared",
                "document.added" to "event_document",
                "document.removed" to "event_removed",
                "document.replaced" to "event_replaced",
                "run.created" to "event_run",
            )[action] ?: "event_updated"),
    )
