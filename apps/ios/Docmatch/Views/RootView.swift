import DocmatchKit
import SwiftUI

/// Sign-in until there is an account, then the three tabs (new run, history, account).
struct RootView: View {
    let session: SessionModel
    @Environment(\.texts) private var texts

    init(session: SessionModel) { self.session = session }

    var body: some View {
        // The ZStack owns the task: on a Group (or on the switch itself) it would run again for
        // every case that appears, and refresh() changes the case.
        ZStack {
            switch session.state {
            case .loading:
                LoadingView(label: texts.t("mobile.loading"))
            case .signedOut(let state):
                LoginView(state: state, session: session)
            case .signedIn:
                SignedInView(session: session)
            }
        }
        .task { await session.refresh() }
    }
}

private enum AppTab: Hashable {
    case new, history, account
}

private struct SignedInView: View {
    let session: SessionModel
    @State private var newRun: NewRunModel
    @State private var history: HistoryModel
    @State private var account: AccountModel
    @State private var tab = AppTab.new
    @State private var newRunPath: [String] = []
    @State private var historyPath: [String] = []
    @Environment(\.texts) private var texts

    init(session: SessionModel) {
        self.session = session
        _newRun = State(initialValue: NewRunModel(dm: session.dm))
        _history = State(initialValue: HistoryModel(dm: session.dm))
        _account = State(initialValue: AccountModel(dm: session.dm) { notice in await session.signedOut(notice: notice) })
    }

    private var tokenExempt: Bool {
        if case .signedIn(let me) = session.state { return me.credits?.tokenExempt == true }
        return false
    }

    var body: some View {
        TabView(selection: $tab) {
            NavigationStack(path: $newRunPath) {
                NewRunView(model: newRun, tokenExempt: tokenExempt) { runId in newRunPath.append(runId) }
                    .navigationDestination(for: String.self) { RunView(dm: session.dm, runId: $0) }
            }
            .tabItem { Label(texts.t("mobile.tab_new"), systemImage: "plus.circle") }
            .tag(AppTab.new)

            NavigationStack(path: $historyPath) {
                HistoryView(model: history) { runId in historyPath.append(runId) }
                    .navigationDestination(for: String.self) { RunView(dm: session.dm, runId: $0) }
            }
            .tabItem { Label(texts.t("mobile.tab_history"), systemImage: "list.bullet") }
            .tag(AppTab.history)

            NavigationStack {
                AccountView(model: account)
            }
            .tabItem { Label(texts.t("mobile.tab_account"), systemImage: "person.crop.circle") }
            .tag(AppTab.account)
        }
    }
}
