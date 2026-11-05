package br.com.docmatch.app.ui

import androidx.activity.compose.LocalActivity
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import br.com.docmatch.app.BuildConfig
import br.com.docmatch.app.auth.GoogleSignIn
import br.com.docmatch.contracts.Limits
import kotlinx.coroutines.launch

private val EMAIL = Regex("^[^\\s@]+@[^\\s@]+\\.[^\\s@]+$")

/** Only the sign-ins the API offers; on a development API the local sign-in comes first. */
@Composable
fun LoginScreen(state: SessionState.SignedOut, session: SessionViewModel, serverUrl: String) {
    val texts = LocalTexts.current
    val activity = LocalActivity.current
    val scope = rememberCoroutineScope()
    var email by rememberSaveable { mutableStateOf(state.email) }
    var code by rememberSaveable(state.challengeId) { mutableStateOf("") }
    var localError by remember { mutableStateOf<String?>(null) }
    val m = state.methods
    val google = m.google && BuildConfig.GOOGLE_SERVER_CLIENT_ID.isNotEmpty()

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).imePadding().padding(DmSize.space3),
        verticalArrangement = Arrangement.spacedBy(DmSize.space2),
    ) {
        Text(t("auth.login_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
        Text(t("auth.login_text"), color = MaterialTheme.colorScheme.onSurfaceVariant)

        if (m.dev) {
            Button(onClick = session::devLogin, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) {
                Text(if (state.busy) t("auth.dev_signing_in") else t("auth.dev_sign_in"))
            }
        }
        if (google) {
            OutlinedButton(
                onClick = {
                    val host = activity ?: return@OutlinedButton
                    scope.launch {
                        runCatching { GoogleSignIn.idToken(host, BuildConfig.GOOGLE_SERVER_CLIENT_ID) }
                            .onSuccess(session::googleIdToken)
                            .onFailure { e -> session.failed(texts.t("mobile.google_failed", mapOf("message" to (e.message ?: "")))) }
                    }
                },
                enabled = !state.busy,
                modifier = Modifier.fillMaxWidth(),
            ) { Text(t("auth.sign_in_google")) }
        }
        if (m.email && (m.dev || google)) {
            HorizontalDivider()
            Text(t("auth.or"), color = MaterialTheme.colorScheme.onSurfaceVariant)
        }

        if (m.email) {
            if (state.challengeId == null) {
                OutlinedTextField(
                    value = email,
                    onValueChange = { email = it.take(Limits.Text.EMAIL) },
                    label = { Text(t("auth.email_label")) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email),
                    modifier = Modifier.fillMaxWidth(),
                )
                Button(
                    onClick = {
                        if (!EMAIL.matches(email.trim())) {
                            localError = texts.t("errors.invalid_email")
                        } else {
                            localError = null
                            session.sendCode(email)
                        }
                    },
                    enabled = !state.busy,
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(t("auth.send_code")) }
            } else {
                Text(t("auth.code_sent", mapOf("email" to state.email)))
                OutlinedTextField(
                    value = code,
                    onValueChange = { v -> code = v.filter(Char::isDigit).take(6) },
                    label = { Text(t("auth.code_label")) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.NumberPassword),
                    modifier = Modifier.fillMaxWidth(),
                )
                Button(
                    onClick = { session.verifyCode(code) },
                    enabled = !state.busy && code.length == 6,
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(t("auth.verify")) }
                TextButton(onClick = session::changeEmail) { Text(t("auth.change_email")) }
            }
        }

        if (!m.any) {
            // Nothing offered usually means the API is out of reach: let the user try again.
            Text(t("auth.none_available"))
            OutlinedButton(onClick = session::refresh, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) { Text(t("mobile.retry")) }
        }
        (localError ?: state.error)?.let {
            Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Assertive })
        }
        if (BuildConfig.DEBUG) {
            Text(t("mobile.server", mapOf("url" to serverUrl)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}
