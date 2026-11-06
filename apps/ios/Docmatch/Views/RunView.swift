import DocmatchKit
import SwiftUI

/// One run: followed while this screen is open, an approval when it parks, the findings by amount
/// at risk with their evidence and decisions, and the report to share.
struct RunView: View {
    private let dm: Docmatch
    @State private var reviewOpen = false
    @State private var changesOpen = false
    @State private var allOpen = false
    @State private var model: RunModel
    @State private var open: String?
    @State private var share: ShareItem?
    @Environment(\.texts) private var texts
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(dm: Docmatch, runId: String) {
        self.dm = dm
        _model = State(initialValue: RunModel(dm: dm, runId: runId))
    }

    var body: some View {
        // The ZStack owns the polling task, so a change of state never restarts it.
        ZStack { content }
            .navigationTitle(texts.t("mobile.run_title"))
            .navigationBarTitleDisplayMode(.inline)
            // Polls only while this screen is shown (and again on retry or after an approval).
            .task(id: model.attempt) { await model.follow() }
            .sheet(item: $share) { item in ActivityView(url: item.url) }
    }

    @ViewBuilder private var content: some View {
        switch model.state {
        case .loading:
            LoadingView(label: texts.t("results.live_loading"))
        case .running(let step):
            VStack(spacing: DmSize.space2) {
                VStack(alignment: .leading, spacing: DmSize.space2) {
                    Text("docmatch").font(.system(.caption, design: .monospaced)).foregroundStyle(.secondary)
                    Text(step.map { Labels.step(texts, $0) } ?? texts.t("mobile.run_waiting"))
                        .font(.system(.body, design: .monospaced))
                        .animation(reduceMotion ? nil : .easeInOut(duration: 0.2), value: step.map { Labels.step(texts, $0) })
                    ProgressView()
                }.padding(DmSize.space3).background(.regularMaterial, in: RoundedRectangle(cornerRadius: DmSize.radiusMd))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .accessibilityElement(children: .combine)
        case .needsApproval(let run, let busy):
            approval(run, busy: busy)
        case .failed(let message):
            ErrorView(message: message) { model.retry() }
        case .done(let run, let findings):
            results(run, findings)
        }
    }

    private func approval(_ run: Run, busy: Bool) -> some View {
        Form {
            Section {
                Text(texts.t("mobile.approve_title"))
                    .font(.title3.bold())
                    .accessibilityAddTraits(.isHeader)
                if let quote = run.pendingQuote {
                    Text(texts.t("mobile.approve_text", ["expected": quote.expectedCredits, "cap": quote.capCredits]))
                    Button(texts.t("mobile.approve")) { Task { await model.approve() } }
                        .disabled(busy)
                } else {
                    Text(texts.t("mobile.status_needs_approval"))
                }
                if let notice = model.notice {
                    Text(notice)
                        .foregroundStyle(Color(DmColors.statusDanger))
                }
            }
        }
    }

    private func results(_ run: Run, _ findings: [Finding]) -> some View {
        let names = Dictionary(run.sources.map { ($0.id, $0.fileName) }, uniquingKeysWith: { a, _ in a })
        let document = run.summary?.reconciliation
        let pending = findings.filter { $0.status == "open" && $0.severity != "info" }
        let critical = findings.filter { $0.severity == "critical" }
        return List {
            Section {
                Text(texts.t(document == nil ? "results.report_ready" : "results.document_ready")).font(.title2.bold())
                Text(texts.t(document == nil ? "results.report_subtitle" : "results.output_description"))
                if let document {
                    Button(texts.t("results.download_corrected")) { export("xlsx") }.buttonStyle(.borderedProminent)
                    Text(texts.t("results.doc_open") + ": " + Format.money(document.totals.openBalance)).bold()
                } else {
                    Text(texts.t("results.legacy_output"))
                    NavigationLink(texts.t("results.generate_corrected")) {
                        ReprocessView(dm: dm)
                    }
                }
                Button(texts.t("results.download_report")) { export("pdf") }.buttonStyle(.bordered)
                DisclosureGroup(texts.t("results.other_formats")) {
                    Button(texts.t(document == nil ? "results.download_findings_csv" : "results.download_data_csv")) { export("csv") }
                    if document == nil { Button(texts.t("results.download_findings_xlsx")) { export("xlsx") } }
                }
                if let warnings = run.summary?.sourceWarnings, !warnings.isEmpty {
                    Text(texts.t("results.source_warnings_title")).bold()
                    ForEach(warnings, id: \.self) { Text("• " + $0).font(.caption) }
                }
                if open == nil, let notice = model.notice { Text(notice) }
            }
            if !critical.isEmpty {
                Section(texts.t("results.critical_heading")) {
                    Text(texts.t("results.critical_scope"))
                    findingRows(critical)
                }
            }
            Section {
                DisclosureGroup(texts.t("results.review_heading") + " · " + String(pending.count), isExpanded: $reviewOpen) {
                    if reviewOpen {
                        Text(texts.t("results.review_scope"))
                        if pending.isEmpty { Text(texts.t("results.review_empty")) }
                        findingRows(pending)
                        if let document { ForEach(document.receivables.filter { $0.proposedAmount > 0 || $0.withholdingPending > 0 }, id: \.id) { DocumentRow(row: $0) } }
                    }
                }
                if let document {
                    DisclosureGroup(texts.t("results.changes_heading"), isExpanded: $changesOpen) {
                        if changesOpen {
                            Text(texts.t("results.doc_scope"))
                            Text(texts.t("results.doc_principal") + ": " + Format.money(document.totals.principalConfirmed))
                            Text(texts.t("results.doc_duplicate") + ": " + Format.money(document.totals.duplicateExcluded))
                            ForEach(document.receivables.filter { $0.appliedPrincipal > 0 || $0.status == "duplicate" }, id: \.id) { DocumentRow(row: $0) }
                        }
                    }
                }
                DisclosureGroup(texts.t("results.all_heading"), isExpanded: $allOpen) {
                    if allOpen {
                        Text(texts.t("results.findings_scope"))
                        if let document {
                            Text(texts.t("results.doc_ledger")).bold()
                            ForEach(document.receivables, id: \.id) { DocumentRow(row: $0) }
                            Text(texts.t("results.doc_bank")).bold()
                            ForEach(Array(document.bankEntries.enumerated()), id: \.offset) { _, entry in
                                VStack(alignment: .leading) {
                                    Text(entry.description + " · " + Format.money(entry.amount))
                                    Text(entry.sourceName + " · " + String(entry.row)).font(.caption)
                                    if entry.proposed { Text(texts.t("results.doc_review")) }
                                }
                            }
                        }
                        findingRows(findings)
                    }
                }
            }
        }
        .sheet(
            item: Binding(
                get: { findings.first { $0.id == open } },
                set: { open = $0?.id })
        ) { finding in
            FindingDetailView(finding: finding, documentRows: document?.receivables.filter { row in finding.evidence.contains { $0.sourceId == row.sourceId && $0.locator?.row == row.sourceRow } } ?? [], names: names, model: model)
        }
    }

    private func findingRows(_ findings: [Finding]) -> some View {
        ForEach(findings) { finding in
            Button { open = finding.id } label: { FindingRow(finding: finding) }.accessibilityIdentifier("finding-row")
        }
    }

    private func export(_ format: String) {
        Task {
            if let url = await model.export(format) { share = ShareItem(url: url) }
        }
    }
}

private struct FindingRow: View {
    let finding: Finding
    @Environment(\.texts) private var texts

    var body: some View {
        let level = Labels.confidence(texts, finding)
        VStack(alignment: .leading, spacing: DmSize.space1) {
            Text(finding.title).bold()
            Text(Format.money(finding.amountAtRisk))
            HStack {
                Text(level)
                    .accessibilityLabel(texts.t("results.confidence_label", ["level": level]))
                Text(Labels.findingStatus(texts, finding.status))
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .foregroundStyle(.primary)
    }
}

private struct FindingDetailView: View {
    let finding: Finding
    let documentRows: [ReconciledReceivable]
    let names: [String: String]
    let model: RunModel
    @State private var editing = false
    @State private var note = ""
    @Environment(\.texts) private var texts
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: DmSize.space2) {
                    Text(finding.title)
                        .font(.title3.bold())
                        .accessibilityAddTraits(.isHeader)
                    Text(texts.t("results.confidence_label", ["level": Labels.confidence(texts, finding)]) + " · " + Labels.findingStatus(texts, finding.status))
                        .foregroundStyle(.secondary)
                    Text(texts.t("results.amount_at_risk") + ": " + Format.money(finding.amountAtRisk))
                        .bold()
                    if !finding.explanation.isEmpty {
                        Text(texts.t("results.explanation")).bold().accessibilityAddTraits(.isHeader)
                        Text(finding.explanation)
                    }
                    Text(texts.t("results.evidence_heading")).bold().accessibilityAddTraits(.isHeader)
                    ForEach(groups) { group in
                        Text(names[group.id] ?? group.id).font(.subheadline.bold())
                        ForEach(group.items.indices, id: \.self) { i in
                            EvidenceRow(evidence: group.items[i])
                        }
                        Divider()
                    }
                    Text(texts.t("results.review_scope")).foregroundStyle(.secondary)
                    Text(texts.t("results.file_effect")).bold()
                    if documentRows.isEmpty { Text(texts.t("results.file_effect_unknown")) }
                    else { ForEach(documentRows, id: \.id) { DocumentRow(row: $0) } }
                    decisions
                    if let notice = model.notice {
                        Text(notice)
                    }
                }
                .padding(DmSize.space3)
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(texts.t("common.close")) { dismiss() }
                }
            }
        }
    }

    /// The evidence by source file, in a stable order.
    private var groups: [EvidenceGroup] {
        Dictionary(grouping: finding.evidence, by: \.sourceId).map { EvidenceGroup(id: $0.key, items: $0.value) }.sorted { $0.id < $1.id }
    }

    @ViewBuilder private var decisions: some View {
        if editing {
            TextField(texts.t("results.edit_label"), text: $note, axis: .vertical)
                .lineLimit(2...6)
                .textFieldStyle(.roundedBorder)
                .onChange(of: note) { _, value in
                    let cut = NewRunModel.cut(value, Limits.Text.findingFeedbackNote)
                    if cut != value { note = cut }
                }
            CounterText(count: NewRunModel.count(note), max: Limits.Text.findingFeedbackNote)
            HStack {
                Button(texts.t("results.edit_save")) {
                    let text = note.trimmingCharacters(in: .whitespacesAndNewlines)
                    editing = false
                    Task { await model.decide(finding, Decision.resolved, note: text) }
                }
                .buttonStyle(.borderedProminent)
                .disabled(note.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                Button(texts.t("results.edit_cancel")) { editing = false }
            }
        } else {
            // Every decision is recorded on the API, so changing it changes it everywhere.
            VStack(alignment: .leading) {
                Button(texts.t("results.accept")) { Task { await model.decide(finding, Decision.confirmed) } }
                    .buttonStyle(.borderedProminent)
                    .disabled(finding.status == Decision.confirmed)
                Button(texts.t("results.reject")) { Task { await model.decide(finding, Decision.dismissed) } }
                    .buttonStyle(.bordered)
                    .disabled(finding.status == Decision.dismissed)
                Button(texts.t("results.edit")) { editing = true }
                    .buttonStyle(.bordered)
            }
        }
    }
}

private struct EvidenceRow: View {
    let evidence: Evidence

    var body: some View {
        let v = evidence.values
        VStack(alignment: .leading) {
            Text([Format.date(v?.date), v?.description ?? evidence.label ?? evidence.excerpt ?? ""].filter { !$0.isEmpty }.joined(separator: " · "))
            if let amount = v?.amount {
                Text(Format.money(amount)).bold()
            }
            if let document = v?.document {
                Text(document).font(.caption)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// The evidence of one source file (its id).
private struct EvidenceGroup: Identifiable {
    let id: String
    let items: [Evidence]
}

private struct DocumentRow: View {
    let row: ReconciledReceivable
    @Environment(\.texts) private var texts
    var body: some View {
        VStack(alignment: .leading, spacing: DmSize.space1) {
            Text(row.reference + " · " + row.customer).bold()
            Text(texts.t("results.file_balance_change", ["before": Format.money(row.originalAmount), "after": Format.money(row.openAmount)]))
            Text(texts.t("results.doc_source", ["name": row.sourceName, "row": row.sourceRow])).font(.caption)
            if row.status == "duplicate" { Text(texts.t("results.doc_duplicate_status", ["id": row.duplicateOf])) }
            if row.proposedAmount > 0 { Text(texts.t("results.file_proposed_change", ["amount": Format.money(row.proposedAmount)])) }
            if row.withholdingPending > 0 { Text(texts.t("results.doc_tax_note", ["amount": Format.money(row.withholdingPending)])) }
        }
    }
}

private struct ReprocessView: View {
    let dm: Docmatch
    @State private var model: NewRunModel
    @State private var runID: String?
    init(dm: Docmatch) { self.dm = dm; _model = State(initialValue: NewRunModel(dm: dm)) }
    var body: some View {
        NewRunView(model: model) { runID = $0 }
            .navigationDestination(item: $runID) { RunView(dm: dm, runId: $0) }
    }
}
