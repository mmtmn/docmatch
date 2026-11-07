package br.com.docmatch.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import br.com.docmatch.app.Exports
import br.com.docmatch.app.KeystoreSecretStore
import br.com.docmatch.core.AccountMembership
import br.com.docmatch.core.Docmatch
import br.com.docmatch.core.InvitationLink

private data class Tab(val route: String, val label: String, val icon: ImageVector)

@Composable
fun DocmatchRoot(dm: Docmatch, invitationUrl: String? = null, onInvitationClosed: () -> Unit = {}) {
    CompositionLocalProvider(LocalTexts provides dm.texts) {
        DocmatchTheme {
            val app = LocalContext.current.applicationContext
            val session: SessionViewModel = viewModel {
                SessionViewModel(dm, KeystoreSecretStore(app, "selected-account-v1")) { Exports.clear(app) }
            }
            val token =
                remember(invitationUrl) {
                    invitationUrl?.let { InvitationLink.token(it, dm.baseUrl) }
                }
            if (token != null) {
                val owner = remember(token) { androidx.lifecycle.ViewModelStore() }
                DisposableEffect(owner) { onDispose { owner.clear() } }
                val invitation: InvitationViewModel =
                    viewModel(
                        viewModelStoreOwner =
                            object : androidx.lifecycle.ViewModelStoreOwner {
                                override val viewModelStore = owner
                            },
                    ) {
                        InvitationViewModel(dm, token)
                    }
                InvitationScreen(invitation, session, onInvitationClosed)
            }
            val accounts by session.accounts.collectAsState()
            val lifecycle = LocalLifecycleOwner.current
            DisposableEffect(lifecycle, session) {
                val observer = LifecycleEventObserver { _, event ->
                    if (event == Lifecycle.Event.ON_RESUME) session.validateAccounts()
                }
                lifecycle.lifecycle.addObserver(observer)
                onDispose { lifecycle.lifecycle.removeObserver(observer) }
            }
            when (val s = session.state.collectAsState().value) {
                SessionState.Loading ->
                    Scaffold { p ->
                        Box(Modifier.padding(p).consumeWindowInsets(p)) {
                            Loading(t("mobile.loading"))
                        }
                    }
                is SessionState.SignedOut ->
                    Scaffold(topBar = { DmHeader() }) { p ->
                        Box(Modifier.padding(p).consumeWindowInsets(p)) {
                            LoginScreen(s, session, dm.baseUrl)
                        }
                    }
                is SessionState.SignedIn ->
                    key(accounts.selected, accounts.current?.role) {
                        val current =
                            remember(accounts.selected, accounts.current?.role) {
                                session.currentDM
                            }
                        SignedIn(current, session, accounts, s.me.credits?.tokenExempt == true)
                    }
            }
        }
    }
}

@Composable
private fun SignedIn(
    dm: Docmatch,
    session: SessionViewModel,
    accounts: AccountSelection,
    tokenExempt: Boolean,
) {
    val nav = rememberNavController()
    val work by session.projectWork.collectAsState()
    var choosing by remember { mutableStateOf(false) }
    var target by remember { mutableStateOf<AccountMembership?>(null) }
    val tabs =
        listOf(Tab("projects", t("projects.title"), DmIcons.Folder)) +
            (if (accounts.current?.capabilities?.readResults != false)
                listOf(Tab("history", t("projects.activity"), Icons.AutoMirrored.Filled.List))
            else emptyList()) +
            Tab("account", t("mobile.tab_account"), Icons.Filled.AccountCircle)
    val entry by nav.currentBackStackEntryAsState()
    if (choosing)
        AlertDialog(
            onDismissRequest = { if (!accounts.switching) choosing = false },
            title = { Text(t("projects.choose_account")) },
            text = {
                Column(Modifier.verticalScroll(rememberScrollState())) {
                    accounts.items.forEach { item ->
                        TextButton(
                            {
                                if (work.dirty) target = item
                                else {
                                    session.selectAccount(item.id)
                                    choosing = false
                                }
                            },
                            enabled = !accounts.switching && item.id != accounts.selected,
                        ) {
                            Column(Modifier.fillMaxWidth()) {
                                Text(accountName(item), style = MaterialTheme.typography.titleSmall)
                                Text(accountRole(item.role))
                            }
                        }
                    }
                }
            },
            confirmButton = { TextButton({ choosing = false }) { Text(t("common.cancel")) } },
        )
    target?.let { item ->
        ProjectConfirmation(t("projects.leave_unsaved"), { target = null }) {
            target = null
            choosing = false
            session.selectAccount(item.id)
        }
    }
    Scaffold(
        topBar = {
            Column(
                Modifier.windowInsetsPadding(
                    WindowInsets.safeDrawing.only(
                        WindowInsetsSides.Top + WindowInsetsSides.Horizontal,
                    ),
                ),
            ) {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 52.dp).padding(horizontal = 16.dp),
                    verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                ) {
                    if (
                        entry?.destination?.route?.contains("run/") == true ||
                            entry?.destination?.route == "team"
                    )
                        IconButton({ nav.popBackStack() }) { Icon(DmIcons.Back, t("mobile.back")) }
                    accounts.current?.let { current ->
                        TextButton(
                            { choosing = true },
                            enabled = !accounts.switching && !work.busy,
                        ) {
                            Text(
                                accountName(current),
                                Modifier.weight(1f),
                                style = MaterialTheme.typography.titleMedium,
                            )
                            Icon(DmIcons.Down, t("projects.choose_account"))
                        }
                    } ?: DmBrand()
                }
                accounts.error?.let { DmNotice(it, true) }
                HorizontalDivider()
            }
        },
        bottomBar = {
            NavigationBar(
                containerColor = MaterialTheme.colorScheme.background,
                tonalElevation = 0.dp,
            ) {
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
                        enabled = !work.busy,
                        icon = { Icon(tab.icon, contentDescription = null) },
                        label = { Text(tab.label, style = MaterialTheme.typography.labelMedium) },
                        colors =
                            NavigationBarItemDefaults.colors(
                                indicatorColor = MaterialTheme.colorScheme.primaryContainer,
                            ),
                    )
                }
            }
        },
    ) { padding ->
        NavHost(
            nav,
            startDestination = "projects",
            modifier = Modifier.padding(padding).consumeWindowInsets(padding),
        ) {
            composable("projects") {
                accounts.current?.let { account ->
                    val vm: ProjectsViewModel = viewModel { ProjectsViewModel(dm) }
                    ProjectsScreen(vm, account) { nav.navigate("project/$it") }
                }
                    ?: run {
                        val vm: NewRunViewModel = viewModel { NewRunViewModel(dm) }
                        NewRunScreen(vm, tokenExempt) { nav.navigate("run/$it") }
                    }
            }
            composable("project/{id}") { back ->
                val id = back.arguments?.getString("id").orEmpty()
                val vm: ProjectViewModel =
                    viewModel(key = "project-$id") { ProjectViewModel(dm, id) }
                ProjectScreen(
                    vm,
                    accounts.current?.let { accountName(it) }.orEmpty(),
                    session,
                    { nav.popBackStack() },
                ) {
                    nav.navigate("project/$id/run/$it")
                }
            }
            composable("history") {
                val vm: HistoryViewModel = viewModel {
                    HistoryViewModel(dm, projects = accounts.current != null)
                }
                HistoryScreen(vm) { run ->
                    nav.navigate(
                        run.projectId?.let { "project/$it/run/${run.id}" } ?: "run/${run.id}",
                    )
                }
            }
            composable("account") {
                val vm: AccountViewModel = viewModel { AccountViewModel(dm, session::signedOut) }
                AccountScreen(
                    vm,
                    canManage = accounts.current?.capabilities?.manageAccount != false,
                    onTeam =
                        if (accounts.current != null) {
                            { nav.navigate("team") }
                        } else null,
                )
            }
            composable("team") {
                val vm: TeamViewModel = viewModel { TeamViewModel(dm) }
                TeamScreen(vm, session) { nav.popBackStack() }
            }
            composable("run/{id}") { back ->
                val id = back.arguments?.getString("id").orEmpty()
                val vm: RunViewModel = viewModel(key = "run-$id") { RunViewModel(dm, id) }
                RunScreen(vm) { nav.navigate("projects") }
            }
            composable("project/{project}/run/{id}") { back ->
                val id = back.arguments?.getString("id").orEmpty()
                val project = back.arguments?.getString("project").orEmpty()
                val vm: RunViewModel =
                    viewModel(key = "run-$project-$id") { RunViewModel(dm.inProject(project), id) }
                RunScreen(vm) { nav.navigate("project/$project") }
            }
        }
    }
}

@Composable
internal fun accountName(account: AccountMembership): String =
    if (account.kind == "personal") t("projects.personal_account") else account.name

@Composable
internal fun accountRole(role: String): String =
    t(
        "teams." +
            (mapOf(
                "owner" to "owner",
                "admin" to "admin",
                "uploader" to "upload_only",
                "reconciler" to "full_access",
                "viewer" to "viewer",
            )[role] ?: "legacy_member"),
    )
