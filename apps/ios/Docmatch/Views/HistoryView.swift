import DocmatchKit
import SwiftUI

/// Every run of the account, whichever device started it; reloaded every time the tab is shown.
struct HistoryView: View {
    let model: HistoryModel
    let onOpen: (Run) -> Void
    @Environment(\.texts) private var texts

    init(model: HistoryModel, onOpen: @escaping (Run) -> Void) {
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
                DmPage {
                    DmHeading(title: texts.t("mobile.tab_history"), subtitle: texts.t("mobile.history_text"))
                    if runs.isEmpty {
                        VStack(spacing: 14) {
                            DmIconTile(name: "folder")
                            Text(texts.t("mobile.history_empty")).font(.subheadline).foregroundStyle(Color(DmColors.textMuted)).multilineTextAlignment(.center)
                        }.frame(maxWidth: .infinity).padding(.vertical, 32)
                    }
                    ForEach(runs) { run in
                        Button { onOpen(run) } label: {
                            DmCard {
                                HStack {
                                    Text(Labels.runStatus(texts, run.status)).font(.headline)
                                    Spacer()
                                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(Color(DmColors.textMuted))
                                }
                                Text(texts.t("mobile.history_item", ["date": Format.dateTime(run.createdAt), "count": run.sources.count]))
                                    .font(.subheadline).foregroundStyle(Color(DmColors.textMuted))
                                if let summary = run.summary {
                                    Text(texts.t("mobile.run_findings", ["count": summary.findingsTotal]) + " · " + Format.money(summary.amountAtRisk)).font(.subheadline)
                                }
                            }
                        }.buttonStyle(.plain)
                    }
                    if let notice { DmNotice(message: notice, error: true) }
                    if next != nil {
                        Button(texts.t("mobile.load_more")) { Task { await model.more() } }.buttonStyle(DmButtonStyle(.secondary))
                    }
                }
                .refreshable { await model.refresh() }
            }
        }
        .dmNavigation(texts.t("mobile.tab_history"))
        .task { await model.refresh() }
    }
}
