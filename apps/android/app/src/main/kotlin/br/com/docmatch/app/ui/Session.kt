package br.com.docmatch.app.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.AuthMethods
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.Me
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

/**
 * Who is signed in on this device. Every sign-in lands on the same account as on the site.
 * [cleanup] removes what the session left on the device (exported reports) whenever it ends.
 */
class SessionViewModel(private val dm: Docmatch, private val cleanup: () -> Unit = {}) : ViewModel() {
    private val _state = MutableStateFlow<SessionState>(SessionState.Loading)
    val state: StateFlow<SessionState> = _state

    init {
        refresh()
        // Revoked from another device's account page (or ended on the server): back to sign-in
        // with the API's own message, wherever the user was.
        viewModelScope.launch {
            dm.sessionEnded.collect { e ->
                cleanup()
                if (_state.value is SessionState.SignedIn) _state.value = SessionState.SignedOut(dm.api.methods(), error = e.message)
            }
        }
    }

    fun refresh() {
        viewModelScope.launch {
            _state.value = SessionState.Loading
            _state.value = load()
        }
    }

    /** After a sign-out (here or from another device): back to the sign-in, with a notice. */
    fun signedOut(notice: String? = null) {
        cleanup()
        viewModelScope.launch { _state.value = SessionState.SignedOut(dm.api.methods(), error = notice) }
    }

    fun sendCode(email: String) =
        signingIn(keepEmail = email) {
            val challenge = dm.api.emailStart(email.trim())
            (state.value as? SessionState.SignedOut)?.copy(email = email.trim(), challengeId = challenge.challengeId, busy = false)
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
        (state.value as? SessionState.SignedOut)?.let { _state.value = it.copy(challengeId = null, error = null) }
    }

    fun passwordLogin(email: String, password: String) = signingIn(keepEmail = email) { dm.api.passwordLogin(email.trim(), password).let { null } }

    fun devLogin() = signingIn { dm.api.devLogin().let { null } }

    fun googleIdToken(token: String) = signingIn { dm.api.googleNative(token).let { null } }

    /** A sign-in that ended before the API (the user closed the account picker: [message] null). */
    fun failed(message: String?) {
        (state.value as? SessionState.SignedOut)?.let { _state.value = it.copy(busy = false, error = message) }
    }

    /**
     * Runs a sign-in step. A step that returns a SignedOut state stays on the sign-in screen (the
     * e-mail code); one that returns null signed in, and the account is loaded.
     */
    private fun signingIn(keepEmail: String? = null, step: suspend () -> SessionState.SignedOut?) {
        val current = state.value as? SessionState.SignedOut ?: return
        if (current.busy) return
        _state.value = current.copy(busy = true, error = null, email = keepEmail ?: current.email)
        viewModelScope.launch {
            _state.value =
                try {
                    step() ?: load()
                } catch (e: ApiException) {
                    current.copy(busy = false, error = e.message, email = keepEmail ?: current.email)
                }
        }
    }

    private suspend fun load(): SessionState =
        try {
            dm.api.me()?.let { SessionState.SignedIn(it) } ?: SessionState.SignedOut(dm.api.methods())
        } catch (e: ApiException) {
            SessionState.SignedOut(dm.api.methods(), error = e.message)
        }
}
