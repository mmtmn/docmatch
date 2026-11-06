package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

sealed interface TeamAction {
    data class Convert(val name: String) : TeamAction

    data class Invite(val email: String, val role: String) : TeamAction

    data class Resend(val id: String) : TeamAction

    data class Revoke(val id: String) : TeamAction

    data class Role(val id: String, val role: String) : TeamAction

    data class Remove(val id: String) : TeamAction

    data class Transfer(val id: String) : TeamAction

    data object Leave : TeamAction
}

data class TeamState(
    val team: TeamInfo? = null,
    val busy: Boolean = false,
    val error: String? = null,
    val notice: String? = null,
    val left: Boolean = false,
)

class TeamViewModel(private val dm: Docmatch) : ViewModel() {
    private val _state = MutableStateFlow(TeamState())
    val state: StateFlow<TeamState> = _state

    fun load() {
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val team = dm.api.team()
                _state.update { it.copy(team = team) }
            } catch (e: ApiException) {
                fail(e)
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun perform(action: TeamAction, after: () -> Unit = {}) {
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null, notice = null) }
        viewModelScope.launch {
            try {
                when (action) {
                    is TeamAction.Convert -> dm.api.convertToTeam(action.name)
                    is TeamAction.Invite -> {
                        dm.api.invite(action.email, action.role)
                        _state.update { it.copy(notice = dm.texts.t("teams.invite_sent")) }
                    }
                    is TeamAction.Resend -> {
                        dm.api.resendInvitation(action.id)
                        _state.update { it.copy(notice = dm.texts.t("teams.resent")) }
                    }
                    is TeamAction.Revoke -> dm.api.revokeInvitation(action.id)
                    is TeamAction.Role -> dm.api.changeMemberRole(action.id, action.role)
                    is TeamAction.Remove -> dm.api.removeMember(action.id)
                    is TeamAction.Transfer -> dm.api.transferOwnership(action.id)
                    TeamAction.Leave -> {
                        dm.api.leaveTeam()
                        _state.update { it.copy(team = null, left = true) }
                        after()
                        return@launch
                    }
                }
                val team = dm.api.team()
                _state.update { it.copy(team = team) }
                after()
            } catch (e: ApiException) {
                fail(e)
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    private fun fail(e: ApiException) {
        _state.update {
            it.copy(
                team = if (e.status in listOf(401, 403, 404)) null else it.team,
                error = e.message,
            )
        }
    }
}

@Composable
fun TeamScreen(vm: TeamViewModel, session: SessionViewModel, onBack: () -> Unit) {
    val s by vm.state.collectAsState()
    var name by rememberSaveable { mutableStateOf("") }
    var email by rememberSaveable { mutableStateOf("") }
    var role by rememberSaveable { mutableStateOf("uploader") }
    var confirm by remember { mutableStateOf<Pair<TeamAction, String>?>(null) }
    val texts = LocalTexts.current
    LaunchedEffect(Unit) { vm.load() }
    LaunchedEffect(s.left) { if (s.left) onBack() }
    confirm?.let { (action, message) ->
        ProjectConfirmation(message, { confirm = null }) {
            confirm = null
            vm.perform(action, session::validateAccounts)
        }
    }
    LazyColumn(
        contentPadding = PaddingValues(DmSize.space3),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        item { DmHeading(t("teams.title"), t("teams.no_charge")) }
        s.team?.let { team ->
            item { Text(team.name, style = MaterialTheme.typography.headlineSmall) }
            if (team.kind == "personal" && team.capabilities.manageAccount)
                item {
                    DmHeading(t("teams.convert_title"), t("teams.convert_body"))
                    OutlinedTextField(
                        name,
                        { name = it },
                        label = { Text(t("teams.name")) },
                        modifier = Modifier.fillMaxWidth(),
                        enabled = !s.busy,
                    )
                    DmPrimaryButton(
                        {
                            confirm =
                                TeamAction.Convert(name) to
                                    texts.t("teams.convert_confirm", mapOf("name" to name))
                        },
                        enabled = !s.busy && name.isNotBlank(),
                    ) {
                        Text(t("teams.convert"))
                    }
                }
            if (team.kind == "team" && team.capabilities.manageAccount) {
                item {
                    DmCard {
                        Column(
                            Modifier.padding(DmSize.space2),
                            verticalArrangement = Arrangement.spacedBy(DmSize.space2),
                        ) {
                            Text(t("teams.invite"), style = MaterialTheme.typography.titleMedium)
                            OutlinedTextField(
                                email,
                                { email = it },
                                label = { Text(t("teams.email")) },
                                keyboardOptions =
                                    KeyboardOptions(keyboardType = KeyboardType.Email),
                                modifier = Modifier.fillMaxWidth(),
                                enabled = !s.busy,
                            )
                            ProjectToggle(t("teams.full_access"), role == "reconciler", !s.busy) {
                                role = if (it) "reconciler" else "uploader"
                            }
                            Text(
                                t(
                                    if (role == "uploader") "teams.upload_explanation"
                                    else "teams.full_explanation",
                                ),
                            )
                            Text(
                                t("teams.accept_required"),
                                style = MaterialTheme.typography.bodySmall,
                            )
                            DmPrimaryButton(
                                { vm.perform(TeamAction.Invite(email.trim(), role)) },
                                enabled = !s.busy && team.canInvite && email.contains('@'),
                            ) {
                                Text(t("teams.send_invite"))
                            }
                            if (!team.canInvite) Text(t("teams.subscription_required"))
                            Text(
                                t(
                                    "teams.members_limit",
                                    mapOf(
                                        "count" to
                                            team.members.size +
                                                team.invitations.count { it.status == "pending" },
                                        "max" to team.memberLimit,
                                    ),
                                ),
                                style = MaterialTheme.typography.bodySmall,
                            )
                        }
                    }
                }
                item {
                    Text(t("teams.sharing_reminder"))
                    Text(t("teams.members"), style = MaterialTheme.typography.titleMedium)
                }
                items(team.members, key = { it.id }) { member ->
                    DmCard {
                        Column(
                            Modifier.padding(DmSize.space2),
                            verticalArrangement = Arrangement.spacedBy(DmSize.space1),
                        ) {
                            Text(member.email, style = MaterialTheme.typography.titleSmall)
                            Text(accountRole(member.role))
                            if (member.role in listOf("uploader", "reconciler")) {
                                val next =
                                    if (member.role == "uploader") "reconciler" else "uploader"
                                val nextLabel = accountRole(next)
                                TextButton(
                                    {
                                        confirm =
                                            TeamAction.Role(member.id, next) to
                                                texts.t(
                                                    "teams.change_role_confirm",
                                                    mapOf(
                                                        "email" to member.email,
                                                        "role" to nextLabel,
                                                    ),
                                                )
                                    },
                                    enabled = !s.busy,
                                ) {
                                    Text(nextLabel)
                                }
                                TextButton(
                                    {
                                        confirm =
                                            TeamAction.Remove(member.id) to
                                                texts.t(
                                                    "teams.remove_confirm",
                                                    mapOf("email" to member.email),
                                                )
                                    },
                                    enabled = !s.busy,
                                ) {
                                    Text(t("teams.remove"), color = MaterialTheme.colorScheme.error)
                                }
                                if (team.role == "owner" && member.role == "reconciler")
                                    TextButton(
                                        {
                                            confirm =
                                                TeamAction.Transfer(member.id) to
                                                    texts.t(
                                                        "teams.transfer_confirm",
                                                        mapOf("email" to member.email),
                                                    )
                                        },
                                        enabled = !s.busy,
                                    ) {
                                        Text(t("teams.transfer"))
                                    }
                            }
                        }
                    }
                }
                item {
                    Text(t("teams.invitations"), style = MaterialTheme.typography.titleMedium)
                    if (team.invitations.isEmpty()) Text(t("teams.no_invitations"))
                }
                items(team.invitations, key = { it.id }) { invite ->
                    DmCard {
                        Column(
                            Modifier.padding(DmSize.space2),
                            verticalArrangement = Arrangement.spacedBy(DmSize.space1),
                        ) {
                            Text(invite.email, style = MaterialTheme.typography.titleSmall)
                            Text(t("teams.${invite.status}") + " · " + accountRole(invite.role))
                            Text(
                                t(
                                    "teams.expires",
                                    mapOf("date" to Format.dateTime(invite.expiresAt)),
                                ),
                            )
                            if (invite.status in listOf("pending", "expired")) {
                                TextButton(
                                    { vm.perform(TeamAction.Resend(invite.id)) },
                                    enabled = !s.busy,
                                ) {
                                    Text(t("teams.resend"))
                                }
                                TextButton(
                                    {
                                        confirm =
                                            TeamAction.Revoke(invite.id) to
                                                texts.t(
                                                    "teams.revoke_confirm",
                                                    mapOf("email" to invite.email),
                                                )
                                    },
                                    enabled = !s.busy,
                                ) {
                                    Text(t("teams.revoke"))
                                }
                            }
                        }
                    }
                }
            }
            if (team.kind == "team" && team.role != "owner")
                item {
                    Text(t("teams.leave_explanation"))
                    TextButton(
                        {
                            confirm =
                                TeamAction.Leave to
                                    texts.t("teams.leave_confirm", mapOf("name" to team.name))
                        },
                        enabled = !s.busy,
                    ) {
                        Text(t("teams.leave"), color = MaterialTheme.colorScheme.error)
                    }
                }
        }
        s.notice?.let { item { DmNotice(it) } }
        s.error?.let {
            item {
                DmNotice(it, true)
                TextButton(vm::load) { Text(t("projects.retry_load")) }
            }
        }
        if (s.busy) item { CircularProgressIndicator() }
    }
}
