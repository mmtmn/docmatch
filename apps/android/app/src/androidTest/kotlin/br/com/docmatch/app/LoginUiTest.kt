package br.com.docmatch.app

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Regression: password login is the default and revealing a password never sends an email code. */
@RunWith(AndroidJUnit4::class)
class LoginUiTest {
    @get:Rule val compose = createEmptyComposeRule()

    @Test
    fun passwordVisibilityAndExplicitEmailCodeChoice() {
        assumeTrue(InstrumentationRegistry.getArguments().getString("docmatchLocal") == "1")
        val app = ApplicationProvider.getApplicationContext<DocmatchApplication>()
        val dm = app.docmatch
        dm.cookies.clear()
        fun text(key: String) = dm.texts.t(key)
        ActivityScenario.launch(MainActivity::class.java).use {
            val password = hasText(text("auth.password_label")) and hasSetTextAction()
            compose.waitUntil(30_000) { compose.onAllNodes(password).fetchSemanticsNodes().isNotEmpty() }
            compose.onNode(password).performScrollTo().performTextInput("local-ui-test-password")
            compose.onNode(password).assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.Password))
            compose.onNodeWithContentDescription(text("auth.show_password")).performClick()
            compose.onNode(password).assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, androidx.compose.ui.text.AnnotatedString("local-ui-test-password")))
            compose.onNodeWithContentDescription(text("auth.hide_password")).performClick()
            compose.onNode(password).assert(SemanticsMatcher("password is masked") {
                val value = it.config[SemanticsProperties.EditableText].text
                value.isNotEmpty() && value.all { character -> character == '•' }
            })
            compose.onNodeWithText(text("auth.send_code")).performScrollTo().performClick()
            compose.onAllNodes(password).assertCountEquals(0)
            compose.onNodeWithText(text("auth.use_password")).performScrollTo().performClick()
            compose.onNode(password).assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, androidx.compose.ui.text.AnnotatedString("")))
        }
    }
}
