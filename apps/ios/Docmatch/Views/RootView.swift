import DocmatchKit
import SwiftUI

/// One identity, with a separate navigation tree for each selected account and permission.
struct RootView: View {
    let session: SessionModel
    @State private var invitationToken: String?
    @StateObject private var safariAuthorization = SafariAuthorization()
    @Environment(\.texts) private var texts
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        ZStack {
            switch session.state {
            case .loading:
                LoadingView(label: texts.t("mobile.loading"))
            case .signedOut(let state):
                VStack(spacing: 0) {
                    HStack {
                        DmBrand()
                        Spacer()
                    }.padding(.horizontal, 20).padding(.vertical, 14)
                    Divider()
                    LoginView(state: state, session: session)
                }
            case .signedIn:
                SignedInView(session: session)
                    .id((session.accountID ?? "personal") + ":" + (session.currentAccount?.role ?? "owner"))
            }
        }
        .background(Color(DmColors.bgCanvas)).foregroundStyle(Color(DmColors.textPrimary))
        .task { await session.refresh() }
        .onOpenURL { url in
            if ["docmatch-extension-dev", "br.com.docmatch.extension"].contains(url.scheme) {
                invitationToken = nil
                safariAuthorization.open(url)
            } else if let token = InvitationLink.token(url, site: AppConfig.shared.apiBaseURL) {
                invitationToken = token
            }
        }
        .sheet(isPresented: Binding(get: { invitationToken != nil }, set: { if !$0 { invitationToken = nil } })) {
            if let invitationToken { InvitationView(session: session, token: invitationToken) }
        }
        .sheet(isPresented: $safariAuthorization.presented) {
            VStack(alignment: .leading, spacing: 20) {
                Text(texts.t("extensions.native_title")).font(.title.bold())
                Text(safariAuthorization.status)
                Text(texts.t("extensions.native_scope")).foregroundStyle(.secondary)
                Button(texts.t("common.close")) { safariAuthorization.presented = false }
            }.padding(24).presentationDetents([.medium])
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await session.validateAccounts() } }
        }
    }
}

private enum AppTab: Hashable { case projects, activity, account }
private struct ActivityRunLink: Hashable {
    let id: String
    let projectID: String?
}

private struct SignedInView: View {
    let session: SessionModel
    @State private var newRun: NewRunModel
    @State private var history: HistoryModel
    @State private var account: AccountModel
    @State private var tab = AppTab.projects
    @State private var choosingAccount = false
    @State private var proposedAccount: AccountMembership?
    @Environment(\.texts) private var texts

    init(session: SessionModel) {
        self.session = session
        let dm = session.currentDM
        _newRun = State(initialValue: NewRunModel(dm: dm))
        _history = State(initialValue: HistoryModel(dm: dm, projects: session.currentAccount != nil))
        _account = State(initialValue: AccountModel(dm: dm) { notice in await session.signedOut(notice: notice) })
    }
    private var canRead: Bool { session.currentAccount?.capabilities.readResults ?? true }
    private var canManage: Bool { session.currentAccount?.capabilities.manageAccount ?? true }
    private var tokenExempt: Bool {
        if case .signedIn(let me) = session.state { return me.credits?.tokenExempt == true }
        return false
    }
    private func accountName(_ account: AccountMembership) -> String {
        account.kind == "personal" ? texts.t("projects.personal_account") : account.name
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                if let current = session.currentAccount {
                    Button {
                        choosingAccount = true
                    } label: {
                        Label(accountName(current), systemImage: "chevron.down").font(.headline).multilineTextAlignment(
                            .leading)
                    }.accessibilityLabel(texts.t("projects.choose_account") + ": " + accountName(current))
                        .disabled(session.switching || session.hasActiveProjectWork)
                } else {
                    DmBrand()
                }
                Spacer()
                if session.switching { ProgressView() }
            }.padding(.horizontal, 20).padding(.vertical, 12)
            if let error = session.accountError { DmNotice(message: error, error: true).padding(.horizontal, 20) }
            Divider()
            TabView(selection: $tab) {
                NavigationStack {
                    if let membership = session.currentAccount {
                        ProjectsView(dm: session.currentDM, account: membership, session: session)
                    } else {
                        LegacyNewRunView(model: newRun, dm: session.currentDM, tokenExempt: tokenExempt)
                    }
                }
                .tabItem { Label(texts.t("projects.title"), systemImage: "folder") }.tag(AppTab.projects)
                if canRead {
                    NavigationStack {
                        ActivityList(model: history, dm: session.currentDM)
                    }
                    .tabItem { Label(texts.t("projects.activity"), systemImage: "list.bullet") }.tag(AppTab.activity)
                }
                NavigationStack {
                    AccountView(model: account, canManage: canManage, session: session)
                }
                .tabItem { Label(texts.t("mobile.tab_account"), systemImage: "person.crop.circle") }.tag(AppTab.account)
            }
            .toolbarBackground(Color(DmColors.bgCanvas), for: .tabBar)
            .toolbarBackground(.visible, for: .tabBar)
        }
        .sheet(isPresented: $choosingAccount) {
            NavigationStack {
                List(session.accounts) { item in
                    Button {
                        if session.hasUnsavedProjectChanges {
                            proposedAccount = item
                        } else {
                            Task {
                                await session.selectAccount(item.id)
                                choosingAccount = false
                            }
                        }
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 6) {
                                Text(accountName(item)).font(.headline)
                                Text(Labels.accountRole(texts, item.role)).font(.subheadline)
                            }
                            Spacer()
                            if item.id == session.accountID {
                                Image(systemName: "checkmark").accessibilityLabel(texts.t("projects.selected_account"))
                            }
                        }
                    }.disabled(session.switching || item.id == session.accountID)
                }
                .navigationTitle(texts.t("projects.choose_account"))
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button(texts.t("common.cancel")) { choosingAccount = false }
                    }
                }
                .confirmationDialog(
                    texts.t("projects.leave_unsaved"),
                    isPresented: Binding(get: { proposedAccount != nil }, set: { if !$0 { proposedAccount = nil } }),
                    titleVisibility: .visible
                ) {
                    Button(texts.t("projects.discard_and_continue"), role: .destructive) {
                        if let target = proposedAccount {
                            Task {
                                await session.selectAccount(target.id)
                                choosingAccount = false
                            }
                        }
                        proposedAccount = nil
                    }
                }
            }
        }
    }
}

private struct ActivityList: View {
    let model: HistoryModel
    let dm: Docmatch
    @State private var result: ActivityRunLink?
    var body: some View {
        HistoryView(model: model) { result = ActivityRunLink(id: $0.id, projectID: $0.projectId) }
            .navigationDestination(isPresented: Binding(get: { result != nil }, set: { if !$0 { result = nil } })) {
                if let result { RunView(dm: result.projectID.map { dm.inProject($0) } ?? dm, runId: result.id) }
            }
    }
}

private struct LegacyNewRunView: View {
    let model: NewRunModel
    let dm: Docmatch
    let tokenExempt: Bool
    @State private var result: String?
    var body: some View {
        NewRunView(model: model, tokenExempt: tokenExempt) { result = $0 }
            .navigationDestination(isPresented: Binding(get: { result != nil }, set: { if !$0 { result = nil } })) {
                if let result { RunView(dm: dm, runId: result) }
            }
    }
}
