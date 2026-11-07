import DocmatchKit
import SwiftUI

/// Every run of the account, whichever device started it; reloaded every time the tab is shown.
struct HistoryView: View {
    let model: HistoryModel
    let onOpen: (Run) -> Void
    let accountName: String
    let onSaved: (String) -> Void
    @State private var importing: Run?
    @State private var importName = ""
    @State private var importBusy = false
    @State private var importError: String?
    @State private var importTask: Task<Void, Never>?
    @Environment(\.texts) private var texts

    init(model: HistoryModel, accountName: String = "", onSaved: @escaping (String) -> Void = { _ in }, onOpen: @escaping (Run) -> Void) {
        self.model = model
        self.onOpen = onOpen
        self.accountName = accountName
        self.onSaved = onSaved
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
                        if run.canSaveAsProject {
                            Button(texts.t("projects.import_action")) {
                                importName = texts.t("projects.import_name", ["date": Format.dateTime(run.createdAt)])
                                importError = nil
                                importing = run
                            }.buttonStyle(DmButtonStyle(.secondary))
                        }
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
        .onDisappear { importTask?.cancel() }
        .sheet(item: $importing) { run in
            NavigationStack {
                DmPage {
                    DmHeading(title: texts.t("projects.import_action"), subtitle: texts.t("projects.import_explanation", ["account": accountName]))
                    TextField(texts.t("projects.name"), text: $importName).textFieldStyle(.roundedBorder).disabled(importBusy)
                    if let importError { DmNotice(message: importError, error: true) }
                    Button(texts.t(importBusy ? "projects.saving" : "projects.import_action")) {
                        importBusy = true
                        importError = nil
                        importTask = Task {
                            do {
                                let project = try await model.saveAsProject(run.id, name: importName)
                                guard !Task.isCancelled else { return }
                                importing = nil
                                onSaved(project.id)
                            } catch {
                                guard !Task.isCancelled else { return }
                                importError = (error as? ApiError)?.message ?? texts.t("errors.unknown")
                            }
                            importBusy = false
                        }
                    }.buttonStyle(DmButtonStyle(.primary)).disabled(importBusy || importName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    Button(texts.t("common.cancel")) { importing = nil }.buttonStyle(DmButtonStyle(.secondary)).disabled(importBusy)
                }
            }.presentationDetents([.medium, .large]).interactiveDismissDisabled(importBusy)
        }
    }
}
