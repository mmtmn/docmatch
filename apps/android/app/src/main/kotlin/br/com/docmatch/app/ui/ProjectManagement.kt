package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import br.com.docmatch.core.*
import java.util.UUID
import kotlinx.coroutines.launch

@Composable
fun ProjectSharingSection(vm: ProjectViewModel) {
    val state by vm.state.collectAsState()
    val project = state.project ?: return
    var members by remember { mutableStateOf<List<TeamMember>>(emptyList()) }
    var selected by remember { mutableStateOf(project.sharedWith.orEmpty().toSet()) }
    var visibility by remember { mutableStateOf(project.visibility) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirming by remember { mutableStateOf(false) }
    LaunchedEffect(project.id) {
        try {
            members = vm.dm.api.team().members.filter { it.role !in listOf("owner", "admin") }
        } catch (e: ApiException) {
            error = e.message
        }
    }
    if (confirming)
        ProjectConfirmation(
            t(if (visibility == "team") "projects.share_confirm" else "projects.sharing_confirm"),
            { confirming = false },
        ) {
            confirming = false
            vm.share(visibility, selected.sorted())
        }
    DmDisclosure(t("projects.sharing")) {
        Text(t("projects.sharing_explanation"))
        ProjectToggle(t("projects.visibility_team"), visibility == "team", !state.busy) {
            visibility = if (it) "team" else "private"
        }
        if (visibility == "private")
            members.forEach { member ->
                ProjectToggle(
                    member.email + " · " + accountRole(member.role),
                    selected.contains(member.id),
                    !state.busy,
                ) {
                    selected = if (it) selected + member.id else selected - member.id
                }
            }
        TextButton({ confirming = true }, enabled = !state.busy) {
            Text(t("projects.sharing_save"))
        }
        error?.let { DmNotice(it, true) }
    }
}

@Composable
fun ProjectRulesSection(vm: ProjectViewModel) {
    val state by vm.state.collectAsState()
    var rules by remember { mutableStateOf<List<ProjectRule>>(emptyList()) }
    var next by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var editor by remember { mutableStateOf(false) }
    var editing by remember { mutableStateOf<ProjectRule?>(null) }
    var promotion by remember { mutableStateOf<ProjectRule?>(null) }
    val scope = rememberCoroutineScope()
    fun change(rule: ProjectRule) {
        rules =
            if (rules.any { it.id == rule.id }) rules.map { if (it.id == rule.id) rule else it }
            else listOf(rule) + rules
    }
    fun load(more: Boolean = false) {
        if (busy) return
        busy = true
        error = null
        scope.launch {
            try {
                val page = vm.dm.api.projectRules(vm.id, if (more) next else null)
                rules = if (more) (rules + page.items).distinctBy { it.id } else page.items
                next = page.nextCursor
            } catch (e: ApiException) {
                error = e.message
            } finally {
                busy = false
            }
        }
    }
    fun promote(rule: ProjectRule, to: String) {
        if (busy) return
        busy = true
        error = null
        scope.launch {
            try {
                change(
                    vm.dm.api.promoteProjectRule(vm.id, rule.id, to, UUID.randomUUID().toString()),
                )
                if (to == "retired")
                    vm.settings(
                        state.settings.copy(
                            ruleIds = state.settings.ruleIds.filter { it != rule.id },
                        ),
                    )
            } catch (e: ApiException) {
                error = e.message
            } finally {
                busy = false
            }
        }
    }
    LaunchedEffect(vm.id) { load() }
    promotion?.let { rule ->
        ProjectConfirmation(
            t(
                if (rule.status == "candidate") "projects.rule_verify_confirm"
                else "projects.rule_trust_confirm",
                mapOf("title" to rule.title),
            ),
            { promotion = null },
        ) {
            promotion = null
            promote(rule, if (rule.status == "candidate") "verified" else "trusted")
        }
    }
    if (editor)
        ProjectRuleEditor(vm, editing, { editor = false }) { rule ->
            change(rule)
            editor = false
        }
    DmDisclosure(t("projects.saved_rules")) {
        Text(t("projects.rules_review_explanation"))
        if (rules.isEmpty()) Text(t("projects.no_saved_rules"))
        rules.forEach { rule ->
            DmCard {
                Column(
                    Modifier.padding(DmSize.space2),
                    verticalArrangement = Arrangement.spacedBy(DmSize.space1),
                ) {
                    Text(rule.title, style = MaterialTheme.typography.titleMedium)
                    Text(
                        t("projects.rule_${rule.status}") +
                            " · " +
                            t("projects.rule_version", mapOf("version" to rule.version)),
                        style = MaterialTheme.typography.bodySmall,
                    )
                    Text(
                        t(
                            if (rule.provenance.origin == "user") "projects.rule_by_person"
                            else "projects.rule_by_analysis",
                        ),
                        style = MaterialTheme.typography.bodySmall,
                    )
                    Text(rule.body)
                    ProjectToggle(
                        t("projects.rule_select"),
                        state.settings.ruleIds.contains(rule.id),
                        !busy && !state.busy && rule.status != "retired",
                    ) { selected ->
                        vm.settings(
                            state.settings.copy(
                                ruleIds =
                                    if (selected) state.settings.ruleIds + rule.id
                                    else state.settings.ruleIds.filter { it != rule.id },
                            ),
                        )
                    }
                    if (rule.status != "retired") {
                        TextButton(
                            {
                                editing = rule
                                editor = true
                            },
                            enabled = !busy && !state.busy,
                        ) {
                            Text(t("projects.rule_save_version"))
                        }
                        if (rule.status in listOf("candidate", "verified"))
                            TextButton({ promotion = rule }, enabled = !busy && !state.busy) {
                                Text(
                                    t(
                                        if (rule.status == "candidate") "projects.rule_verify"
                                        else "projects.rule_trust",
                                    ),
                                )
                            }
                        TextButton({ promote(rule, "retired") }, enabled = !busy && !state.busy) {
                            Text(t("projects.rule_retire"))
                        }
                    }
                }
            }
        }
        if (next != null) TextButton({ load(true) }, enabled = !busy) { Text(t("projects.more")) }
        TextButton(
            {
                editing = null
                editor = true
            },
            enabled = !busy && !state.busy,
        ) {
            Text(t("projects.rule_new"))
        }
        if (busy) CircularProgressIndicator()
        error?.let { DmNotice(it, true) }
    }
}

@Composable
private fun ProjectRuleEditor(
    vm: ProjectViewModel,
    initial: ProjectRule?,
    onCancel: () -> Unit,
    onSave: (ProjectRule) -> Unit,
) {
    var title by remember { mutableStateOf(initial?.title.orEmpty()) }
    var body by remember { mutableStateOf(initial?.body.orEmpty()) }
    var key by remember { mutableStateOf(UUID.randomUUID().toString()) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { if (!busy) onCancel() },
        title = {
            Text(t(if (initial == null) "projects.rule_new" else "projects.rule_save_version"))
        },
        text = {
            Column(
                Modifier.verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(DmSize.space2),
            ) {
                OutlinedTextField(
                    title,
                    {
                        title = it
                        key = UUID.randomUUID().toString()
                    },
                    label = { Text(t("projects.rule_title")) },
                    enabled = !busy,
                )
                OutlinedTextField(
                    body,
                    {
                        body = it
                        key = UUID.randomUUID().toString()
                    },
                    label = { Text(t("projects.rule_body")) },
                    minLines = 4,
                    enabled = !busy,
                )
                error?.let { DmNotice(it, true) }
            }
        },
        confirmButton = {
            TextButton(
                {
                    busy = true
                    error = null
                    scope.launch {
                        try {
                            onSave(vm.dm.api.saveProjectRule(vm.id, initial?.id, title, body, key))
                        } catch (e: ApiException) {
                            error = e.message
                        } finally {
                            busy = false
                        }
                    }
                },
                enabled = !busy && title.isNotBlank() && body.isNotBlank(),
            ) {
                Text(t("projects.rule_save"))
            }
        },
        dismissButton = { TextButton(onCancel, enabled = !busy) { Text(t("common.cancel")) } },
    )
}
