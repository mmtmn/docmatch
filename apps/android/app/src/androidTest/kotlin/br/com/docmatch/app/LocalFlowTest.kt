package br.com.docmatch.app

import android.app.Activity
import android.app.Instrumentation
import android.content.ClipData
import android.content.Intent
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.core.content.FileProvider
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.Intents.intending
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import br.com.docmatch.app.ui.TAG_DEVICE
import br.com.docmatch.app.ui.TAG_FINDING
import br.com.docmatch.core.Decision
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.MemorySecretStore
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The whole app against `make local` (DM_DEV_LOGIN=1, no provider key): the local sign-in, the
 * public Vantara twin reconciled to its answer key, a decision, this phone in the account's
 * devices, then the same account from a second device (the site in a browser), which sees the
 * same run and decision and ends this phone's session; the phone goes back to sign-in.
 *
 * Opt-in, since it needs the local stack: make -C apps/android e2e-local
 */
@RunWith(AndroidJUnit4::class)
class LocalFlowTest {
    @get:Rule val compose = createEmptyComposeRule()

    private val app = ApplicationProvider.getApplicationContext<DocmatchApplication>()
    private val dm: Docmatch get() = app.docmatch
    private val fixtures = InstrumentationRegistry.getInstrumentation().context.assets

    @Before
    fun setUp() {
        assumeTrue("só com make local: -e docmatchLocal 1", InstrumentationRegistry.getArguments().getString("docmatchLocal") == "1")
        dm.cookies.clear()
        Intents.init()
    }

    @After
    fun tearDown() {
        runCatching { Intents.release() }
    }

    @Test
    fun reconcilesTheTwinAndKeepsTheAccountConsistentAcrossDevices() {
        ActivityScenario.launch(MainActivity::class.java).use {
            // No key and no account: the local sign-in.
            click(hasText(t("auth.dev_sign_in")))
            await(hasText(t("common.app_title")))

            // The twin's inputs through the system picker (answered with this app's own provider).
            intending(hasAction(Intent.ACTION_OPEN_DOCUMENT)).respondWith(Instrumentation.ActivityResult(Activity.RESULT_OK, pickedInputs()))
            click(hasText(t("mobile.pick_files")))
            INPUTS.forEach { await(hasText(it, substring = true)) }
            click(hasTestTag("submit-run"))

            // The estimate, confirmed; the run, to the answer key.
            click(hasText(t("form.submit")) and hasAnyAncestor(isDialog()), timeoutMs = RUN_TIMEOUT_MS)
            await(hasText(t("mobile.run_findings", mapOf("count" to expectedFindings()))), timeoutMs = RUN_TIMEOUT_MS)

            // Downloads are visible before opening the progressively disclosed details.
            await(hasText(t("results.download_corrected")))
            await(hasText(t("results.download_report")))
            // A decision, recorded on the API. Critical alerts remain visible even when collapsed.
            compose.onAllNodesWithTag(TAG_FINDING).onFirst().performClick()
            if (InstrumentationRegistry.getArguments().getString("docmatchRequireModel") == "1") {
                await(hasText("Próximo passo sugerido:", substring = true))
            }
            click(hasText(t("results.accept")))
            await(hasText(t("mobile.finding_decided")))
            click(hasText(t("common.close")))

            // This phone is the current device of the account.
            val phone = runBlocking { dm.api.devices() }.single { it.current }
            assertEquals("android", phone.platform)
            click(hasText(t("mobile.tab_account")))
            await(hasTestTag(TAG_DEVICE) and hasAnyDescendant(hasText(phone.name)))
            await(hasText(t("account.this_device")))
            val runId = runBlocking { dm.api.runs() }.items.first().id

            // The same account from another device: the same run, the same decision, both devices.
            val texts = app.assets.open("pt-BR.json").use { it.readBytes().decodeToString() }
            val browser = Docmatch(dm.baseUrl, MemorySecretStore(), BROWSER_USER_AGENT, texts, aliasLoopback = true)
            runBlocking {
                browser.api.devLogin()
                assertTrue(browser.api.runs().items.any { it.id == runId }, "o outro aparelho vê a mesma conciliação")
                assertEquals(1, browser.api.findings(runId).count { it.status == Decision.CONFIRMED }, "e a mesma decisão")
                if (InstrumentationRegistry.getArguments().getString("docmatchRequireModel") == "1") {
                    assertTrue(browser.api.findings(runId).all { it.explanation.contains("Próximo passo sugerido:") }, "a análise da IA foi persistida em todos os achados")
                }
                val devices = browser.api.devices()
                assertEquals("web", devices.single { it.current }.platform)
                val seen = devices.single { it.id == phone.id }
                assertEquals(phone.name, seen.name)
                // ...and ends this phone's session from its account page.
                browser.api.revokeDevice(seen)
            }

            // The phone notices on its next request and goes back to sign-in.
            click(hasText(t("mobile.tab_history")))
            await(hasText(t("auth.dev_sign_in")))
            assertTrue(!dm.cookies.hasSession(), "a sessão encerrada saiu do aparelho")
            runBlocking { browser.api.logout() }
        }
    }

    /** Optional recording path: real network and public input fixtures, no billing or private data. */
    @Test
    fun recordPublicTwinDemo() {
        assumeTrue(InstrumentationRegistry.getArguments().getString("docmatchRecord") == "1")
        ActivityScenario.launch(MainActivity::class.java).use {
            click(hasText(t("auth.dev_sign_in")))
            await(hasText(t("common.app_title")))
            Thread.sleep(1500)
            intending(hasAction(Intent.ACTION_OPEN_DOCUMENT)).respondWith(Instrumentation.ActivityResult(Activity.RESULT_OK, pickedInputs()))
            click(hasText(t("mobile.pick_files")))
            INPUTS.forEach { await(hasText(it, substring = true)) }
            Thread.sleep(2000)
            click(hasTestTag("submit-run"))
            await(hasText(t("form.submit")) and hasAnyAncestor(isDialog()), RUN_TIMEOUT_MS)
            Thread.sleep(2000)
            click(hasText(t("form.submit")) and hasAnyAncestor(isDialog()))
            await(hasText(t("mobile.run_findings", mapOf("count" to expectedFindings()))), RUN_TIMEOUT_MS)
            await(hasText(t("results.download_corrected")))
            Thread.sleep(2500)
            val runId = runBlocking { dm.api.runs() }.items.first().id
            val findings = runBlocking { dm.api.findings(runId) }
            assertEquals(expectedFindings(), findings.size)
            if (InstrumentationRegistry.getArguments().getString("docmatchRequireModel") == "1") {
                assertTrue(findings.all { it.explanation.contains("Próximo passo sugerido:") })
            }
            android.util.Log.i("DOCMATCH_DEMO", "completed=" + runId)
            compose.onAllNodesWithTag(TAG_FINDING).onFirst().performClick()
            Thread.sleep(3000)
            click(hasText(t("common.close")))
            Thread.sleep(1500)
        }
    }

    private fun t(key: String, vars: Map<String, Any> = emptyMap()) = dm.texts.t(key, vars)

    private fun await(matcher: SemanticsMatcher, timeoutMs: Long = UI_TIMEOUT_MS) {
        compose.waitUntil(timeoutMs) { compose.onAllNodes(matcher).fetchSemanticsNodes().isNotEmpty() }
    }

    private fun click(matcher: SemanticsMatcher, timeoutMs: Long = UI_TIMEOUT_MS) {
        await(matcher, timeoutMs)
        val node = compose.onAllNodes(matcher).onFirst()
        if (!node.isDisplayed()) node.performScrollTo()
        node.performClick()
    }

    /** The twin's inputs copied to the cache and offered through the app's FileProvider. */
    private fun pickedInputs(): Intent {
        val dir = File(app.cacheDir, "exports/e2e").apply { mkdirs() }
        val uris =
            INPUTS.map { name ->
                val file = File(dir, name)
                fixtures.open(name).use { input -> file.outputStream().use { input.copyTo(it) } }
                FileProvider.getUriForFile(app, "${app.packageName}.exports", file)
            }
        val clip = ClipData.newRawUri(null, uris.first()).apply { uris.drop(1).forEach { addItem(ClipData.Item(it)) } }
        return Intent().apply {
            clipData = clip
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
    }

    /** Entries of the answer key's `findings:` section, as the site's local e2e counts them. */
    private fun expectedFindings(): Int {
        val yaml = fixtures.open("expected.yaml").use { it.readBytes().decodeToString() }
        val section = yaml.split(Regex("^findings:\\s*$", RegexOption.MULTILINE)).getOrNull(1)?.split(Regex("^[a-z_]+:", RegexOption.MULTILINE))?.first().orEmpty()
        return Regex("^ {2}- \\{ code: F\\d+", RegexOption.MULTILINE).findAll(section).count().also { check(it > 0) { "gabarito sem achados" } }
    }

    private companion object {
        val INPUTS =
            listOf(
                "01_contas_a_receber_protheus_jul2026.csv",
                "02_titulos_unidade_norte_omie_jul2026.csv",
                "03_extrato_bancario_jul2026.csv",
                "04_caixa_de_entrada_contas_a_pagar.txt",
            )
        const val UI_TIMEOUT_MS = 30_000L
        const val RUN_TIMEOUT_MS = 240_000L
        const val BROWSER_USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
    }
}
