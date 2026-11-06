package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.ui.unit.dp
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import br.com.docmatch.app.Exports
import br.com.docmatch.core.Docmatch

private data class Tab(val route: String, val label: String, val icon: ImageVector)

/** The app: sign-in until there is an account, then the three tabs and the run screen. */
@Composable
fun DocmatchRoot(dm: Docmatch) {
    CompositionLocalProvider(LocalTexts provides dm.texts) {
        DocmatchTheme {
            val app = LocalContext.current.applicationContext
            // Whenever the session ends (here or from another device) its exports go too.
            val session: SessionViewModel = viewModel { SessionViewModel(dm) { Exports.clear(app) } }
            when (val s = session.state.collectAsState().value) {
                SessionState.Loading -> Scaffold { p -> androidx.compose.foundation.layout.Box(Modifier.padding(p).consumeWindowInsets(p)) { Loading(t("mobile.loading")) } }
                is SessionState.SignedOut -> Scaffold(topBar = { DmHeader() }) { p -> androidx.compose.foundation.layout.Box(Modifier.padding(p).consumeWindowInsets(p)) { LoginScreen(s, session, dm.baseUrl) } }
                is SessionState.SignedIn -> SignedIn(dm, s.me.credits?.tokenExempt == true) { notice -> session.signedOut(notice) }
            }
        }
    }
}

@Composable
private fun SignedIn(dm: Docmatch, tokenExempt: Boolean, onSignedOut: (String?) -> Unit) {
    val nav = rememberNavController()
    val tabs =
        listOf(
            Tab("new", t("mobile.tab_new"), Icons.Filled.Add),
            Tab("history", t("mobile.tab_history"), Icons.AutoMirrored.Filled.List),
            Tab("account", t("mobile.tab_account"), Icons.Filled.AccountCircle),
        )
    val entry by nav.currentBackStackEntryAsState()
    Scaffold(
        topBar = { DmHeader(title = if (entry?.destination?.route == "new") t("common.app_title") else null,
            onBack = if (entry?.destination?.route == "run/{id}") { { nav.popBackStack(); Unit } } else null) },
        bottomBar = {
            NavigationBar(containerColor = MaterialTheme.colorScheme.background, tonalElevation = 0.dp) {
                tabs.forEach { tab ->
                    NavigationBarItem(
                        selected = entry?.destination?.route == tab.route,
                        onClick = {
                            nav.navigate(tab.route) {
                                popUpTo(nav.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(tab.icon, contentDescription = null) },
                        label = { Text(tab.label, style = MaterialTheme.typography.labelMedium) },
                        colors = NavigationBarItemDefaults.colors(indicatorColor = MaterialTheme.colorScheme.primaryContainer),
                    )
                }
            }
        },
    ) { padding ->
        NavHost(nav, startDestination = "new", modifier = Modifier.padding(padding).consumeWindowInsets(padding)) {
            composable("new") {
                val vm: NewRunViewModel = viewModel { NewRunViewModel(dm) }
                NewRunScreen(vm, tokenExempt) { runId -> nav.navigate("run/$runId") }
            }
            composable("history") {
                val vm: HistoryViewModel = viewModel { HistoryViewModel(dm) }
                HistoryScreen(vm) { runId -> nav.navigate("run/$runId") }
            }
            composable("account") {
                val vm: AccountViewModel = viewModel { AccountViewModel(dm, onSignedOut) }
                AccountScreen(vm)
            }
            composable("run/{id}") { back ->
                val id = back.arguments?.getString("id").orEmpty()
                val vm: RunViewModel = viewModel(key = "run-$id") { RunViewModel(dm, id) }
                RunScreen(vm) { nav.navigate("new") }
            }
        }
    }
}
