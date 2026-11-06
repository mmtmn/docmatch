package br.com.docmatch.app.ui

import androidx.activity.compose.LocalActivity
import androidx.compose.ui.Alignment
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.Row
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.material.icons.Icons
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.ui.res.painterResource
import br.com.docmatch.app.R
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
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
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
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
    var password by remember { mutableStateOf("") }
    var showPassword by remember { mutableStateOf(false) }
    var useCode by rememberSaveable { mutableStateOf(false) }
    var localError by remember { mutableStateOf<String?>(null) }
    var providerBusy by remember { mutableStateOf(false) }
    val busy = state.busy || providerBusy
    val m = state.methods
    val google = m.google && BuildConfig.GOOGLE_SERVER_CLIENT_ID.isNotEmpty()
    // What this app can offer: Apple is web-only on Android, Google needs this build's client id.
    val usable = m.email || m.password || m.dev || google

    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).imePadding().padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp), horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        DmIconTile(DmIcons.Check)
        Text(t("auth.login_title"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.semantics { heading() })
        Text(t("auth.login_text"), color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        DmCard(Modifier.widthIn(max = 440.dp).fillMaxWidth()) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {

        if (m.dev) {
            DmPrimaryButton(onClick = session::devLogin, enabled = !busy, modifier = Modifier.fillMaxWidth()) {
                Text(if (state.busy) t("auth.dev_signing_in") else t("auth.dev_sign_in"))
            }
        }
        if (google) {
            DmSecondaryButton(
                onClick = {
                    val host = activity ?: return@DmSecondaryButton
                    providerBusy = true
                    scope.launch {
                        try {
                        when (val outcome = GoogleSignIn.idToken(host, BuildConfig.GOOGLE_SERVER_CLIENT_ID)) {
                            is GoogleSignIn.Outcome.Token -> session.googleIdToken(outcome.idToken)
                            GoogleSignIn.Outcome.Cancelled -> session.failed(null)
                            GoogleSignIn.Outcome.NoAccount -> session.failed(texts.t("mobile.google_no_account"))
                            GoogleSignIn.Outcome.Failed -> session.failed(texts.t("auth.error_generic"))
                        }
                        } finally { providerBusy = false }
                    }
                },
                enabled = !busy,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Image(painterResource(R.drawable.google_mark), contentDescription = null, modifier = Modifier.size(20.dp))
                Spacer(Modifier.width(12.dp))
                Text(t("auth.sign_in_google"))
            }
        }
        if ((m.email || m.password) && (m.dev || google)) {
            androidx.compose.foundation.layout.Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                HorizontalDivider(Modifier.weight(1f))
                Text(t("auth.or"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                HorizontalDivider(Modifier.weight(1f))
            }
        }

        if (m.password && !useCode) {
            OutlinedTextField(value = email, onValueChange = { email = it.take(Limits.Text.EMAIL) }, label = { Text(t("auth.email_label")) }, singleLine = true, enabled = !busy, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email), modifier = Modifier.fillMaxWidth())
            OutlinedTextField(value = password, onValueChange = { password = it.take(Limits.Text.PASSWORD_MAX) }, label = { Text(t("auth.password_label")) }, singleLine = true, enabled = !busy, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password), visualTransformation = if (showPassword) VisualTransformation.None else PasswordVisualTransformation(), trailingIcon = { IconButton(onClick = { showPassword = !showPassword }) { Icon(if (showPassword) DmIcons.EyeOff else DmIcons.Eye, contentDescription = t(if (showPassword) "auth.hide_password" else "auth.show_password")) } }, modifier = Modifier.fillMaxWidth())
            DmPrimaryButton(onClick = { session.passwordLogin(email, password); password = "" }, enabled = !busy && EMAIL.matches(email.trim()) && password.isNotEmpty(), modifier = Modifier.fillMaxWidth()) { Text(t(if (state.busy) "auth.signing_in" else "nav.login")) }
        }
        if (m.email && m.password) {
            TextButton(onClick = { useCode = !useCode; session.changeEmail(); password = "" }, enabled = !busy) { Text(t(if (useCode) "auth.use_password" else "auth.send_code")) }
        }
        if (m.email && (!m.password || useCode)) {
            if (state.challengeId == null) {
                OutlinedTextField(
                    value = email,
                    onValueChange = { email = it.take(Limits.Text.EMAIL) },
                    label = { Text(t("auth.email_label")) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email),
                    modifier = Modifier.fillMaxWidth(),
                )
                DmPrimaryButton(
                    onClick = {
                        if (!EMAIL.matches(email.trim())) {
                            localError = texts.t("errors.invalid_email")
                        } else {
                            localError = null
                            session.sendCode(email)
                        }
                    },
                    enabled = !busy,
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(t("auth.send_code")) }
            } else {
                Text(t("mobile.code_sent", mapOf("email" to state.email)))
                OutlinedTextField(
                    value = code,
                    onValueChange = { v -> code = v.filter(Char::isDigit).take(6) },
                    label = { Text(t("auth.code_label")) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.NumberPassword),
                    modifier = Modifier.fillMaxWidth(),
                )
                DmPrimaryButton(
                    onClick = { session.verifyCode(code) },
                    enabled = !busy && code.length == 6,
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(t("auth.verify")) }
                TextButton(onClick = session::changeEmail, enabled = !busy) { Text(t("auth.change_email")) }
            }
        }

        if (!usable) {
            // Nothing this app can use; usually the API is out of reach: let the user try again.
            Text(t("auth.none_available"))
            DmSecondaryButton(onClick = session::refresh, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Text(t("mobile.retry")) }
        }
        (localError ?: state.error)?.let {
            DmNotice(it, error = true)
        }
        }
        }
        Text(t("mobile.legal_text"), style = MaterialTheme.typography.bodySmall, textAlign = TextAlign.Center, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (BuildConfig.DEBUG) {
            Text(t("mobile.server", mapOf("url" to serverUrl)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}
