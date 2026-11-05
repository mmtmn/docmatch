package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import br.com.docmatch.core.Docmatch

private data class Tab(val route: String, val label: String, val icon: ImageVector)

/** The app: sign-in until there is an account, then the three tabs and the run screen. */
@Composable
fun DocmatchRoot(dm: Docmatch) {
    CompositionLocalProvider(LocalTexts provides dm.texts) {
        DocmatchTheme {
            val session: SessionViewModel = viewModel { SessionViewModel(dm) }
            when (val s = session.state.collectAsState().value) {
                SessionState.Loading -> Scaffold { p -> androidx.compose.foundation.layout.Box(Modifier.padding(p)) { Loading(t("mobile.loading")) } }
                is SessionState.SignedOut -> Scaffold { p -> androidx.compose.foundation.layout.Box(Modifier.padding(p)) { LoginScreen(s, session, dm.baseUrl) } }
                is SessionState.SignedIn -> SignedIn(dm) { notice -> session.signedOut(notice) }
            }
        }
    }
}

@Composable
private fun SignedIn(dm: Docmatch, onSignedOut: (String?) -> Unit) {
    val nav = rememberNavController()
    val tabs =
        listOf(
            Tab("new", t("mobile.tab_new"), Icons.Filled.Add),
            Tab("history", t("mobile.tab_history"), Icons.AutoMirrored.Filled.List),
            Tab("account", t("mobile.tab_account"), Icons.Filled.AccountCircle),
        )
    val entry by nav.currentBackStackEntryAsState()
    Scaffold(
        bottomBar = {
            NavigationBar {
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
                        label = { Text(tab.label) },
                    )
                }
            }
        },
    ) { padding ->
        NavHost(nav, startDestination = "new", modifier = Modifier.padding(padding)) {
            composable("new") {
                val vm: NewRunViewModel = viewModel { NewRunViewModel(dm) }
                NewRunScreen(vm) { runId -> nav.navigate("run/$runId") }
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
                RunScreen(vm)
            }
        }
    }
}
