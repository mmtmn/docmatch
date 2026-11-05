import DocmatchKit
import SwiftUI

/// Every run of the account, whichever device started it; reloaded every time the tab is shown.
struct HistoryView: View {
    let model: HistoryModel
    let onOpen: (String) -> Void
    @Environment(\.texts) private var texts

    init(model: HistoryModel, onOpen: @escaping (String) -> Void) {
        self.model = model
        self.onOpen = onOpen
    }

    var body: some View {
        // The ZStack owns the task (on a Group it would run for each state that appears).
        ZStack {
            switch model.state {
            case .loading:
                LoadingView(label: texts.t("mobile.loading"))
            case .failed(let message):
                ErrorView(message: message) { Task { await model.refresh() } }
            case .ready(let runs, let next, let notice):
                List {
                    if runs.isEmpty {
                        Text(texts.t("mobile.history_empty"))
                    }
                    ForEach(runs) { run in
                        Button {
                            onOpen(run.id)
                        } label: {
                            VStack(alignment: .leading) {
                                Text(Labels.runStatus(texts, run.status)).bold()
                                Text(texts.t("mobile.history_item", ["date": Format.dateTime(run.createdAt), "count": run.sources.count]))
                                if let summary = run.summary {
                                    Text(texts.t("mobile.run_findings", ["count": summary.findingsTotal]) + " · " + Format.money(summary.amountAtRisk))
                                }
                            }
                            .foregroundStyle(.primary)
                        }
                    }
                    if let notice {
                        Text(notice)
                            .foregroundStyle(Color(DmColors.statusDanger))
                    }
                    if next != nil {
                        Button(texts.t("mobile.load_more")) { Task { await model.more() } }
                    }
                }
                .refreshable { await model.refresh() }
            }
        }
        .navigationTitle(texts.t("mobile.tab_history"))
        .task { await model.refresh() }
    }
}
