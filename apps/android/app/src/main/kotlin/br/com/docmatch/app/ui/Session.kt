package br.com.docmatch.app.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.AccountMembership
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.AuthMethods
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Me
import br.com.docmatch.core.organizations
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

sealed interface SessionState {
    data object Loading : SessionState

    /** Not signed in: the sign-ins this deployment offers, and the e-mail step if one is open. */
    data class SignedOut(
        val methods: AuthMethods,
        val email: String = "",
        val challengeId: String? = null,
        val busy: Boolean = false,
        val error: String? = null,
    ) : SessionState

    data class SignedIn(val me: Me) : SessionState
}

data class AccountSelection(
    val items: List<AccountMembership> = emptyList(),
    val selected: String? = null,
    val userId: String? = null,
    val switching: Boolean = false,
    val error: String? = null,
) {
    val current
        get() = items.firstOrNull { it.id == selected }
}

data class ProjectWork(val dirty: Boolean = false, val busy: Boolean = false)

/**
 * Who is signed in on this device. Every sign-in lands on the same account as on the site.
 * [cleanup] removes what the session left on the device (exported reports) whenever it ends.
 */
class SessionViewModel(val dm: Docmatch, private val cleanup: () -> Unit = {}) : ViewModel() {
    private val _state = MutableStateFlow<SessionState>(SessionState.Loading)
    val state: StateFlow<SessionState> = _state

    private val _accounts = MutableStateFlow(AccountSelection())
    val accounts: StateFlow<AccountSelection> = _accounts
    private val _projectWork = MutableStateFlow(ProjectWork())
    val projectWork: StateFlow<ProjectWork> = _projectWork
    val currentDM
        get() = _accounts.value.selected?.let { dm.inAccount(it) } ?: dm

    fun projectWork(dirty: Boolean, busy: Boolean) {
        _projectWork.value = ProjectWork(dirty, busy)
    }

    private var generation = 0

    private fun clearAccounts() {
        generation += 1
        _accounts.value = AccountSelection()
        _projectWork.value = ProjectWork()
        cleanup()
    }

    init {
        refresh()
        // Revoked from another device's account page (or ended on the server): back to sign-in
        // with the API's own message, wherever the user was.
        viewModelScope.launch {
            dm.sessionEnded.collect { e ->
                if (_state.value is SessionState.SignedIn) signedOut(e.message)
            }
        }
    }

    fun refresh() {
        val generation = this.generation
        viewModelScope.launch {
            _state.value = SessionState.Loading
            val next = load()
            if (generation == this@SessionViewModel.generation) _state.value = next
        }
    }

    /** After a sign-out (here or from another device): back to the sign-in, with a notice. */
    fun signedOut(notice: String? = null) {
        clearAccounts()
        val generation = this.generation
        _state.value = SessionState.Loading
        viewModelScope.launch {
            val methods = dm.api.methods()
            if (generation == this@SessionViewModel.generation)
                _state.value = SessionState.SignedOut(methods, error = notice)
        }
    }

    fun sendCode(email: String) =
        signingIn(keepEmail = email) {
            val challenge = dm.api.emailStart(email.trim())
            (state.value as? SessionState.SignedOut)?.copy(
                email = email.trim(),
                challengeId = challenge.challengeId,
                busy = false,
            )
        }

    fun verifyCode(code: String) {
        val current = state.value as? SessionState.SignedOut ?: return
        val challenge = current.challengeId ?: return
        signingIn(keepEmail = current.email) {
            dm.api.emailVerify(challenge, code)
            null
        }
    }

    fun changeEmail() {
        (state.value as? SessionState.SignedOut)?.let {
            _state.value = it.copy(challengeId = null, error = null)
        }
    }

    fun passwordLogin(email: String, password: String) =
        signingIn(keepEmail = email) { dm.api.passwordLogin(email.trim(), password).let { null } }

    fun devLogin() = signingIn { dm.api.devLogin().let { null } }

    fun googleIdToken(token: String) = signingIn { dm.api.googleNative(token).let { null } }

    /** A sign-in that ended before the API (the user closed the account picker: [message] null). */
    fun failed(message: String?) {
        (state.value as? SessionState.SignedOut)?.let {
            _state.value = it.copy(busy = false, error = message)
        }
    }

    /**
     * Runs a sign-in step. A step that returns a SignedOut state stays on the sign-in screen (the
     * e-mail code); one that returns null signed in, and the account is loaded.
     */
    private fun signingIn(keepEmail: String? = null, step: suspend () -> SessionState.SignedOut?) {
        val current = state.value as? SessionState.SignedOut ?: return
        if (current.busy) return
        _state.value = current.copy(busy = true, error = null, email = keepEmail ?: current.email)
        val generation = this.generation
        viewModelScope.launch {
            val next =
                try {
                    val outcome = step()
                    if (generation != this@SessionViewModel.generation) return@launch
                    outcome ?: load()
                } catch (e: ApiException) {
                    current.copy(
                        busy = false,
                        error = e.message,
                        email = keepEmail ?: current.email,
                    )
                }
            if (generation == this@SessionViewModel.generation) _state.value = next
        }
    }

    fun selectAccount(id: String) {
        val before = _accounts.value
        if (_state.value !is SessionState.SignedIn || before.switching || id == before.selected)
            return
        val generation = this.generation
        _accounts.value = before.copy(switching = true, error = null)
        viewModelScope.launch {
            try {
                val available = dm.api.organizations()
                if (generation != this@SessionViewModel.generation) return@launch
                if (available.userId != before.userId || available.items.none { it.id == id }) {
                    val next = load()
                    if (generation == this@SessionViewModel.generation) _state.value = next
                } else {
                    val me = dm.inAccount(id).api.me()
                    if (generation != this@SessionViewModel.generation) return@launch
                    if (me == null) signedOut()
                    else {
                        cleanup()
                        _projectWork.value = ProjectWork()
                        _accounts.value = AccountSelection(available.items, id, available.userId)
                        _state.value = SessionState.SignedIn(me)
                    }
                }
            } catch (e: ApiException) {
                if (generation == this@SessionViewModel.generation)
                    _accounts.value = before.copy(error = e.message)
            } finally {
                if (generation == this@SessionViewModel.generation)
                    _accounts.value = _accounts.value.copy(switching = false)
            }
        }
    }

    fun validateAccounts() {
        if (_state.value !is SessionState.SignedIn || _accounts.value.switching) return
        val generation = this.generation
        viewModelScope.launch {
            try {
                val available = dm.api.organizations()
                if (generation != this@SessionViewModel.generation) return@launch
                val before = _accounts.value
                if (
                    available.userId != before.userId ||
                        available.items.none { it.id == before.selected }
                ) {
                    cleanup()
                    _projectWork.value = ProjectWork()
                    val next = load()
                    if (generation == this@SessionViewModel.generation) _state.value = next
                } else {
                    _accounts.value = before.copy(items = available.items, error = null)
                }
            } catch (e: ApiException) {
                if (generation == this@SessionViewModel.generation)
                    _accounts.value = _accounts.value.copy(error = e.message)
            }
        }
    }

    private suspend fun load(): SessionState {
        val generation = this.generation
        return try {
            try {
                val available = dm.api.organizations()
                if (generation != this.generation) return state.value
                val before = _accounts.value
                val selected =
                    if (
                        before.userId == available.userId &&
                            available.items.any { it.id == before.selected }
                    )
                        before.selected
                    else available.items.firstOrNull()?.id
                if (before.userId != available.userId || before.selected != selected) {
                    cleanup()
                    _projectWork.value = ProjectWork()
                }
                _accounts.value = AccountSelection(available.items, selected, available.userId)
            } catch (e: ApiException) {
                if (generation != this.generation) return state.value
                if (e.status != 404) throw e
                _accounts.value = AccountSelection()
            }
            currentDM.api.me()?.let { SessionState.SignedIn(it) }
                ?: SessionState.SignedOut(dm.api.methods())
        } catch (e: ApiException) {
            SessionState.SignedOut(dm.api.methods(), error = e.message)
        }
    }
}
