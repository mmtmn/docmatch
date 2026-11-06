package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import br.com.docmatch.core.ApiException
import br.com.docmatch.core.Texts

/** Test tags, the app's counterpart of the site's data-testid. */
const val TAG_FINDING = "finding-row"
const val TAG_DEVICE = "device-row"

/** The pt-BR texts of packages/locales, available to every screen. */
val LocalTexts = staticCompositionLocalOf<Texts> { error("Texts não fornecido") }

@Composable
fun t(key: String, vars: Map<String, Any> = emptyMap()): String = LocalTexts.current.t(key, vars)

/** The message an error should show: the API's pt-BR message, or the generic one. */
fun Texts.messageOf(e: Throwable): String = (e as? ApiException)?.message ?: t("errors.unknown")

@Composable
fun Loading(label: String) {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
            CircularProgressIndicator()
            Text(label, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        }
    }
}

@Composable
fun ErrorState(message: String, onRetry: () -> Unit) {
    Box(Modifier.fillMaxSize().padding(DmSize.space3), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(DmSize.space2)) {
            Text(
                message,
                color = MaterialTheme.colorScheme.error,
                textAlign = TextAlign.Center,
                modifier = Modifier.semantics { liveRegion = LiveRegionMode.Assertive },
            )
            DmPrimaryButton(onClick = onRetry) { Text(t("mobile.retry")) }
        }
    }
}
