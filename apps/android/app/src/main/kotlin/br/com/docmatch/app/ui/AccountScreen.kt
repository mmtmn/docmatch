package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Device
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Format
import br.com.docmatch.core.Me
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

sealed interface AccountState {
    data object Loading : AccountState

    data class Ready(val me: Me, val devices: List<Device>, val notice: String? = null) : AccountState

    data class Failed(val message: String) : AccountState
}

/** The same account page as the site's /conta: profile, credits and every signed-in device. */
class AccountViewModel(private val dm: Docmatch, private val onSignedOut: (String?) -> Unit) : ViewModel() {
    private val _state = MutableStateFlow<AccountState>(AccountState.Loading)
    val state: StateFlow<AccountState> = _state

    /** Reloads the account (credits, devices); what is on screen stays while it loads. */
    suspend fun load(notice: String? = null) {
        _state.value =
            try {
                val me = dm.api.me()
                if (me == null) {
                    onSignedOut(dm.texts.t("account.signed_out"))
                    return
                }
                AccountState.Ready(me, dm.api.devices(), notice)
            } catch (e: ApiException) {
                (_state.value as? AccountState.Ready)?.copy(notice = e.message) ?: AccountState.Failed(e.message)
            }
    }

    fun refresh(notice: String? = null) {
        viewModelScope.launch { load(notice) }
    }

    fun revoke(device: Device) {
        viewModelScope.launch {
            try {
                dm.api.revokeDevice(device)
                if (device.current) onSignedOut(dm.texts.t("account.signed_out")) else refresh(dm.texts.t("account.revoked"))
            } catch (e: ApiException) {
                (_state.value as? AccountState.Ready)?.let { _state.value = it.copy(notice = e.message) }
            }
        }
    }

    fun signOut() {
        viewModelScope.launch {
            runCatching { dm.api.logout() }
            onSignedOut(dm.texts.t("account.signed_out"))
        }
    }
}

@Composable
fun AccountScreen(vm: AccountViewModel) {
    val state by vm.state.collectAsState()
    // Every time the tab is shown: credits spent and devices added elsewhere appear.
    LaunchedEffect(Unit) { vm.load() }
    when (val s = state) {
        AccountState.Loading -> Loading(t("account.loading"))
        is AccountState.Failed -> ErrorState(s.message) { vm.refresh() }
        is AccountState.Ready ->
            LazyColumn(contentPadding = PaddingValues(DmSize.space3), verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
                item {
                    Text(t("account.title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
                    s.notice?.let { Text(it, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
                }
                item {
                    Card(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(DmSize.space2), verticalArrangement = Arrangement.spacedBy(DmSize.space1)) {
                            Text(t("account.profile"), fontWeight = FontWeight.SemiBold, modifier = Modifier.semantics { heading() })
                            if (s.me.user.displayName.isNotBlank()) Labeled(t("account.name_label"), s.me.user.displayName)
                            Labeled(t("account.email_label"), s.me.user.email)
                            s.me.credits?.let { Labeled(t("account.credits"), t("account.credits_available", mapOf("available" to it.available))) }
                        }
                    }
                }
                item {
                    Text(t("account.devices"), fontWeight = FontWeight.SemiBold, modifier = Modifier.semantics { heading() })
                    Text(t("account.devices_text"), color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                items(s.devices, key = { it.id }) { d ->
                    Card(Modifier.fillMaxWidth().testTag(TAG_DEVICE)) {
                        Row(Modifier.padding(DmSize.space2), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(d.name, fontWeight = FontWeight.SemiBold)
                                if (d.current) Text(t("account.this_device"), color = MaterialTheme.colorScheme.primary)
                                Text(t("account.last_seen", mapOf("when" to Format.dateTime(d.lastSeenAt))), style = MaterialTheme.typography.bodySmall)
                            }
                            val label = t("account.revoke_label", mapOf("name" to d.name))
                            OutlinedButton(onClick = { vm.revoke(d) }, modifier = Modifier.semantics { contentDescription = label }) {
                                Text(t("account.revoke"))
                            }
                        }
                    }
                }
                item { Button(onClick = vm::signOut) { Text(t("account.sign_out")) } }
            }
    }
}

/** A value with its label, read together. */
@Composable
private fun Labeled(label: String, value: String) {
    Column(Modifier.semantics(mergeDescendants = true) {}) {
        Text(label, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(value)
    }
}
