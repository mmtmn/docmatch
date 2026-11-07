package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class InvitationInboxState(
    val items: List<InvitationPreview> = emptyList(),
    val nextCursor: String? = null,
    val busy: Boolean = false,
    val error: String? = null,
    val notice: String? = null,
)

class InvitationInboxViewModel(private val dm: Docmatch) : ViewModel() {
    private val _state = MutableStateFlow(InvitationInboxState())
    val state: StateFlow<InvitationInboxState> = _state
    fun load(more: Boolean = false) {
        val cursor = if (more) state.value.nextCursor ?: return else null
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val page = dm.api.pendingInvitations(cursor)
                _state.update {
                    it.copy(items = if (more) (it.items + page.items).distinctBy { item -> item.id } else page.items, nextCursor = page.nextCursor)
                }
            } catch (e: Exception) { fail(e) }
            finally { _state.update { it.copy(busy = false) } }
        }
    }
    fun respond(id: String, accept: Boolean, onSuccess: () -> Unit = {}) {
        if (state.value.busy || state.value.items.none { it.id == id && it.emailMatches && it.status == "pending" }) return
        _state.update { it.copy(busy = true, error = null, notice = null) }
        viewModelScope.launch {
            try {
                val response = dm.api.respondToPendingInvitation(id, accept)
                _state.update { it.copy(items = it.items.filterNot { invite -> invite.id == id }, notice = dm.texts.t(if (response.accepted) "teams.inbox_accepted" else "teams.declined_message")) }
                onSuccess()
            } catch (e: Exception) { fail(e) }
            finally { _state.update { it.copy(busy = false) } }
        }
    }
    private fun fail(e: Exception) {
        if (e is CancellationException) throw e
        _state.update {
            if (e is ApiException && e.status in listOf(401, 403)) it.copy(items = emptyList(), nextCursor = null, error = e.message)
            else it.copy(error = (e as? ApiException)?.message ?: dm.texts.t("errors.unknown"))
        }
    }
}

@Composable
fun InvitationInboxScreen(vm: InvitationInboxViewModel, session: SessionViewModel, onClose: () -> Unit) {
    val s by vm.state.collectAsState()
    var pending by remember { mutableStateOf<Pair<InvitationPreview, Boolean>?>(null) }
    LaunchedEffect(vm) { vm.load() }
    Dialog(onDismissRequest = { if (!s.busy) onClose() }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(Modifier.fillMaxSize()) {
            Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing).verticalScroll(rememberScrollState()).padding(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                TextButton(onClose, enabled = !s.busy) { Text(t("common.close")) }
                DmHeading(t("teams.inbox_title"), t("teams.inbox_hint"))
                Text(t("teams.personal_separate"))
                s.notice?.let { DmNotice(it) }
                s.error?.let {
                    DmNotice(it, true)
                    TextButton({ vm.load() }, enabled = !s.busy) { Text(t("projects.retry_load")) }
                }
                if (s.busy) CircularProgressIndicator()
                if (s.items.isEmpty() && !s.busy && s.error == null) {
                    Text(t("teams.inbox_empty"))
                    Text(t("teams.inbox_verify"))
                }
                s.items.forEach { invite ->
                    DmCard {
                        Text(invite.organization, style = MaterialTheme.typography.headlineSmall)
                        Text(accountRole(invite.role), style = MaterialTheme.typography.titleMedium)
                        Text(t(if (invite.role == "uploader") "teams.upload_explanation" else "teams.full_explanation"))
                        Text(t("teams.expires", mapOf("date" to Format.dateTime(invite.expiresAt))))
                        DmPrimaryButton({ pending = invite to true }, enabled = !s.busy) { Text(t("teams.accept_invitation")) }
                        TextButton({ pending = invite to false }, enabled = !s.busy) { Text(t("teams.decline_invitation")) }
                    }
                }
                if (s.nextCursor != null) TextButton({ vm.load(true) }, enabled = !s.busy) { Text(t("teams.inbox_more")) }
            }
        }
        pending?.let { (invite, accept) ->
            ProjectConfirmation(t(if (accept) "teams.inbox_accept" else "teams.inbox_decline", mapOf("name" to invite.organization)), { pending = null }) {
                pending = null
                vm.respond(invite.id, accept) { session.validateAccounts() }
            }
        }
    }
}
