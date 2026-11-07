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
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class InvitationState(
    val preview: InvitationPreview? = null,
    val response: InvitationResponse? = null,
    val busy: Boolean = false,
    val error: String? = null,
)

class InvitationViewModel(private val dm: Docmatch, private val token: String) : ViewModel() {
    private val _state = MutableStateFlow(InvitationState())
    val state: StateFlow<InvitationState> = _state

    fun load() {
        if (state.value.busy) return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val preview = dm.api.invitation(token)
                _state.update { it.copy(preview = preview) }
            } catch (e: ApiException) {
                _state.update { it.copy(preview = null, error = e.message) }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun respond(accept: Boolean) {
        val preview = state.value.preview ?: return
        if (
            state.value.busy ||
                !preview.emailMatches ||
                (preview.status != "pending" && !(accept && preview.status == "accepted"))
        )
            return
        _state.update { it.copy(busy = true, error = null) }
        viewModelScope.launch {
            try {
                val response = dm.api.respondToInvitation(token, accept)
                _state.update { it.copy(response = response) }
            } catch (e: ApiException) {
                _state.update { it.copy(error = e.message) }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    fun switchLogin(session: SessionViewModel) {
        viewModelScope.launch {
            runCatching { dm.api.logout() }
            session.signedOut()
        }
    }
}

@Composable
fun InvitationScreen(vm: InvitationViewModel, session: SessionViewModel, onClose: () -> Unit) {
    val s by vm.state.collectAsState()
    val identity by session.state.collectAsState()
    val accounts by session.accounts.collectAsState()
    LaunchedEffect(identity) { vm.load() }
    Dialog(
        onDismissRequest = { if (!s.busy) onClose() },
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Surface(Modifier.fillMaxSize()) {
            Column(
                Modifier.fillMaxSize()
                    .windowInsetsPadding(WindowInsets.safeDrawing)
                    .verticalScroll(rememberScrollState())
                    .padding(DmSize.space3),
                verticalArrangement = Arrangement.spacedBy(DmSize.space2),
            ) {
                TextButton(onClose, enabled = !s.busy) { Text(t("common.cancel")) }
                DmHeading(t("teams.invitation_title"), t("teams.personal_separate"))
                s.preview?.let { invite ->
                    Text(invite.organization, style = MaterialTheme.typography.headlineSmall)
                    Text(t("teams.invited_email", mapOf("email" to invite.emailHint)))
                    Text(accountRole(invite.role), style = MaterialTheme.typography.titleMedium)
                    Text(
                        t(
                            if (invite.role == "uploader") "teams.upload_explanation"
                            else "teams.full_explanation",
                        ),
                    )
                    Text(t("teams.expires", mapOf("date" to Format.dateTime(invite.expiresAt))))
                    val response = s.response
                    when {
                        response != null ->
                            if (response.accepted) {
                                DmPrimaryButton(
                                    {
                                        session.selectAccount(response.organizationId)
                                        onClose()
                                    },
                                    enabled = !accounts.switching,
                                ) {
                                    Text(t("teams.open_team"))
                                }
                            } else Text(t("teams.declined_message"))
                        invite.status !in listOf("pending", "accepted") ->
                            DmNotice(t("teams.${invite.status}"))
                        identity is SessionState.SignedIn ->
                            if (invite.emailMatches) {
                                DmPrimaryButton({ vm.respond(true) }, enabled = !s.busy) {
                                    Text(
                                        t(
                                            if (invite.status == "accepted") "teams.open_team"
                                            else "teams.accept_invitation",
                                        ),
                                    )
                                }
                                if (invite.status == "pending")
                                    TextButton({ vm.respond(false) }, enabled = !s.busy) {
                                        Text(t("teams.decline_invitation"))
                                    }
                            } else {
                                DmNotice(t("teams.invite_email_mismatch"))
                                TextButton({ vm.switchLogin(session) }, enabled = !s.busy) {
                                    Text(t("teams.switch_login"))
                                }
                            }
                    }
                }
                val signedOut = identity as? SessionState.SignedOut
                if (signedOut != null) {
                    Text(t("teams.sign_in_invited"))
                    LoginScreen(signedOut, session, session.dm.baseUrl, embedded = true)
                }
                s.error?.let { DmNotice(it, true) }
                if (s.busy) CircularProgressIndicator()
            }
        }
    }
}
