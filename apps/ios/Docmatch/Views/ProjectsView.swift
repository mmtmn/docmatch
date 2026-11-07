import DocmatchKit
import SwiftUI
import UniformTypeIdentifiers

struct ProjectLink: Hashable { let id: String }
struct ProjectRunLink: Hashable { let id: String }

struct ProjectsView: View {
    let dm: Docmatch
    let account: AccountMembership
    let session: SessionModel
    @State private var model: ProjectsModel
    @State private var newName = ""
    @State private var newProject: String?
    @Environment(\.texts) private var texts
    init(dm: Docmatch, account: AccountMembership, session: SessionModel) {
        self.session = session
        self.dm = dm
        self.account = account
        _model = State(initialValue: ProjectsModel(dm: dm))
    }
    var body: some View {
        DmPage {
            DmHeading(title: texts.t("projects.title"), subtitle: texts.t("projects.intro"))
            if account.capabilities.createProject {
                DmCard {
                    TextField(texts.t("projects.name"), text: $newName, prompt: Text(texts.t("projects.name_example")))
                        .dmField().accessibilityLabel(texts.t("projects.name"))
                    Button(texts.t("projects.new")) {
                        Task {
                            if let created = await model.create(name: newName) {
                                newProject = created.id
                                newName = ""
                            }
                        }
                    }.buttonStyle(DmButtonStyle()).disabled(
                        model.busy || newName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            Toggle(
                texts.t("projects.show_archived"),
                isOn: Binding(
                    get: { model.archived },
                    set: {
                        model.archived = $0
                        Task { await model.load() }
                    })
            ).disabled(model.busy)
            if model.items.isEmpty && !model.busy {
                Text(texts.t(account.capabilities.createProject ? "projects.empty_body" : "projects.empty_upload_only"))
            }
            ForEach(model.items) { project in
                NavigationLink(value: ProjectLink(id: project.id)) {
                    DmCard {
                        HStack {
                            Text(project.name).font(.headline)
                            Spacer()
                            Image(systemName: "chevron.right").accessibilityHidden(true)
                        }
                        Text(texts.t("projects.documents_count", ["count": project.documentCount])).font(.subheadline)
                        Text(texts.t("projects.updated", ["date": Format.dateTime(project.updatedAt)])).font(.caption)
                            .foregroundStyle(Color(DmColors.textMuted))
                    }
                }.buttonStyle(.plain)
            }
            if let error = model.error { DmNotice(message: error, error: true) }
            if model.busy { ProgressView().frame(maxWidth: .infinity) }
            if model.next != nil {
                Button(texts.t("projects.more")) { Task { await model.load(more: true) } }.disabled(model.busy)
            }
        }
        .navigationTitle("").toolbar(.hidden, for: .navigationBar)
        .task { await model.load() }
        .refreshable { await model.load() }
        .navigationDestination(for: ProjectLink.self) {
            ProjectView(
                dm: dm, id: $0.id,
                accountName: account.kind == "personal" ? texts.t("projects.personal_account") : account.name,
                session: session)
        }
        .navigationDestination(isPresented: Binding(get: { newProject != nil }, set: { if !$0 { newProject = nil } })) {
            if let newProject {
                ProjectView(
                    dm: dm, id: newProject,
                    accountName: account.kind == "personal" ? texts.t("projects.personal_account") : account.name,
                    session: session)
            }
        }
    }
}

struct ProjectView: View {
    @State private var model: ProjectModel
    let accountName: String
    let session: SessionModel
    @Environment(\.dismiss) private var dismiss
    @State private var leaving = false
    @Environment(\.scenePhase) private var scenePhase
    @State private var section = "documents"
    @State private var picking = false
    @State private var replacing: ProjectDocument?
    @State private var removing: ProjectDocument?
    @State private var showVersions = false
    @State private var filesExpanded = false
    @State private var result: String?
    @State private var discard = false
    @State private var managementAction: String?
    @State private var recoveringDraft: ProjectDraft?
    @State private var discardingDraft: ProjectDraft?
    @Environment(\.texts) private var texts
    init(dm: Docmatch, id: String, accountName: String, session: SessionModel) {
        self.session = session
        _model = State(initialValue: ProjectModel(dm: dm, id: id, recovery: session.draftEditor(projectID: id)))
        self.accountName = accountName
    }
    var body: some View {
        DmPage {
            if let project = model.project {
                Text(accountName).font(.subheadline).foregroundStyle(Color(DmColors.textMuted))
                TextField(texts.t("projects.name"), text: Binding(get: { model.name }, set: { model.rename($0) })).font(
                    .title2.bold()
                ).dmField().disabled(!model.canEdit || model.busy)
                Text(saveText).font(.caption).accessibilityAddTraits(.updatesFrequently)
                if model.canEdit && !model.drafts.isEmpty { recoveryCopies(project) }
                if model.recoveryFailed { DmNotice(message: texts.t("projects.recovery_unavailable"), error: true) }
                if model.localSaved && model.saveState == .offline { DmNotice(message: texts.t("projects.recovery_stored")) }
                if model.saveState == .conflict {
                    DmNotice(message: texts.t(model.localSaved ? "projects.conflict_recovery" : "projects.conflict_help"), error: true)
                    Button(texts.t("projects.reload")) { discard = true }
                } else if model.saveState == .offline {
                    Button(texts.t("projects.retry_save")) { Task { await model.flush() } }
                }
                if !project.capabilities.readResults { DmNotice(message: texts.t("projects.upload_permission")) }
                Picker(texts.t("projects.sections"), selection: $section) {
                    Text(texts.t("projects.documents")).tag("documents")
                    if project.capabilities.readResults { Text(texts.t("projects.results")).tag("results") }
                    Text(texts.t("projects.activity")).tag("activity")
                }.pickerStyle(.segmented)
                switch section {
                case "results": results
                case "activity":
                    ForEach(model.events) { event in
                        Text(eventLabel(event.action) + " · " + Format.dateTime(event.createdAt)).font(.subheadline)
                    }
                default: documents(project)
                }
                if project.capabilities.manageAccount {
                    ProjectSharingView(model: model)
                }
                if project.capabilities.reconcile {
                    DisclosureGroup(texts.t("projects.manage")) {
                        Button(texts.t(project.archivedAt == nil ? "projects.archive" : "projects.restore")) {
                            managementAction = project.archivedAt == nil ? "archive" : "restore"
                        }
                        Button(texts.t("projects.delete"), role: .destructive) { managementAction = "delete" }
                    }.disabled(model.busy)
                }
            } else if !model.busy {
                Button(texts.t("projects.retry_load")) { Task { await model.load() } }
            }
            if model.busy { ProgressView().frame(maxWidth: .infinity) }
            if let error = model.error { DmNotice(message: error, error: true) }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if section == "documents" && model.canEdit {
                VStack(spacing: 8) {
                    Text(texts.t("projects.no_duplicate_cost")).font(.caption).foregroundStyle(
                        Color(DmColors.textMuted))
                    Button(texts.t("projects.reconcile")) { Task { await model.prepare() } }
                        .accessibilityIdentifier("submit-run").buttonStyle(DmButtonStyle())
                        .disabled(
                            model.busy || model.saveState == .saving || model.saveState == .conflict
                                || activeDocuments.isEmpty)
                }
                .frame(maxWidth: 720).padding(.horizontal, 20).padding(.vertical, 12)
                .frame(maxWidth: .infinity).background(Color(DmColors.bgCanvas))
                .overlay(alignment: .top) { Divider() }
            }
        }
        .navigationTitle(texts.t("projects.title")).navigationBarTitleDisplayMode(.inline).toolbar(
            .visible, for: .navigationBar
        )
        .task { await model.load() }
        .navigationBarBackButtonHidden(true)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button(texts.t("projects.back")) {
                    if model.hasUnsavedChanges { leaving = true } else { dismiss() }
                }.disabled(model.busy || model.saveState == .saving)
            }
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await model.validateAccess() } }
            else { model.preserveDraft() }
        }
        .onChange(of: model.hasUnsavedChanges) { _, value in session.hasUnsavedProjectChanges = value }
        .onChange(of: model.busy) { _, value in session.hasActiveProjectWork = value }
        .onDisappear { model.preserveDraft(); session.hasActiveProjectWork = false }
        .confirmationDialog(texts.t("projects.leave_unsaved"), isPresented: $leaving, titleVisibility: .visible) {
            Button(texts.t("projects.discard_and_continue"), role: .destructive) {
                model.discardPendingEdits()
                session.hasUnsavedProjectChanges = false
                dismiss()
            }
        }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.item], allowsMultipleSelection: replacing == nil) {
            response in
            let replacement = replacing?.id
            replacing = nil
            if case .success(let urls) = response {
                Task { await model.add(urls.map { NewRunView.picked($0) }, replacesID: replacement) }
            }
        }
        .confirmationDialog(
            texts.t("projects.remove_document_confirm", ["name": removing?.fileName ?? ""]),
            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
            titleVisibility: .visible
        ) {
            Button(texts.t("projects.remove_document"), role: .destructive) {
                if let doc = removing { Task { await model.remove(doc) } }
                removing = nil
            }
        }
        .confirmationDialog(texts.t(model.localSaved ? "projects.conflict_recovery" : "projects.conflict_help"), isPresented: $discard, titleVisibility: .visible) {
            Button(texts.t("projects.reload")) { Task { await model.load(discardEdits: true) } }
        }
        .confirmationDialog(texts.t("projects.recovery_confirm"),
            isPresented: Binding(get: { recoveringDraft != nil }, set: { if !$0 { recoveringDraft = nil } }), titleVisibility: .visible) {
            Button(texts.t("common.confirm")) {
                if let draft = recoveringDraft { Task { await model.restoreDraft(draft) } }
                recoveringDraft = nil
            }
        }
        .confirmationDialog(texts.t("projects.recovery_discard_confirm"),
            isPresented: Binding(get: { discardingDraft != nil }, set: { if !$0 { discardingDraft = nil } }), titleVisibility: .visible) {
            Button(texts.t("projects.recovery_discard"), role: .destructive) {
                if let draft = discardingDraft { model.discardDraft(draft) }
                discardingDraft = nil
            }
        }
        .sheet(isPresented: Binding(get: { model.analysis?.quote != nil }, set: { if !$0 { model.dismissApproval() } }))
        {
            DmPage {
                DmHeading(
                    title: texts.t("mobile.confirm_title"),
                    subtitle: texts.t("projects.confirm_context", ["account": accountName, "project": model.name]))
                if let quote = model.analysis?.quote { Text(Format.confirmRun(texts, quote)).font(.headline) }
                Button(texts.t("common.confirm")) { Task { await model.approve() } }.accessibilityIdentifier(
                    "confirm-run"
                ).buttonStyle(DmButtonStyle()).disabled(model.busy)
                if let error = model.error { DmNotice(message: error, error: true) }
                Button(texts.t("common.cancel")) { model.dismissApproval() }.disabled(model.busy)
            }.interactiveDismissDisabled(model.busy)
        }
        .confirmationDialog(
            texts.t("projects." + (managementAction ?? "archive") + "_confirm", ["name": model.name]),
            isPresented: Binding(get: { managementAction != nil }, set: { if !$0 { managementAction = nil } }),
            titleVisibility: .visible
        ) {
            Button(texts.t("common.confirm"), role: managementAction == "delete" ? .destructive : nil) {
                let action = managementAction
                managementAction = nil
                Task {
                    if action == "delete" { await model.delete() } else { await model.archive(action == "archive") }
                }
            }
        }
        .onChange(of: model.deleted) { _, deleted in if deleted { dismiss() } }
        .onChange(of: model.openedRun) { _, id in
            if let id {
                result = id
                model.openedRunShown()
            }
        }
        .navigationDestination(isPresented: Binding(get: { result != nil }, set: { if !$0 { result = nil } })) {
            if let result { RunView(dm: model.dm, runId: result) }
        }
    }
    private func recoveryCopies(_ project: SavedProject) -> some View {
        DisclosureGroup(texts.t("projects.recovery_title", ["count": model.drafts.count])) {
            Text(texts.t("projects.recovery_help")).font(.caption)
            ForEach(model.drafts) { draft in
                VStack(alignment: .leading, spacing: 8) {
                    Text(Date(timeIntervalSince1970: Double(draft.savedAt) / 1000).formatted(date: .abbreviated, time: .shortened)).font(.caption)
                    if draft.baseVersion != project.version { Text(texts.t("projects.recovery_changed")) }
                    Text(texts.t("projects.recovery_server")).bold()
                    Text(project.name).textSelection(.enabled)
                    Text(recoverySettings(project.settings ?? ProjectSettings())).textSelection(.enabled)
                    Text(texts.t("projects.recovery_local")).bold()
                    Text(draft.name).textSelection(.enabled)
                    Text(recoverySettings(draft.settings)).textSelection(.enabled)
                    Button(texts.t("projects.recovery_apply")) { recoveringDraft = draft }
                    Button(texts.t("projects.recovery_discard")) { discardingDraft = draft }
                }.disabled(model.busy || model.hasUnsavedChanges)
                Divider()
            }
        }
    }
    private func recoverySettings(_ value: ProjectSettings) -> String {
        var lines = [value.rulesText.isEmpty ? texts.t("projects.recovery_no_rules") : value.rulesText,
         texts.t("form.use_rules") + ": " + texts.t(value.useRules ? "projects.recovery_on" : "projects.recovery_off"),
         texts.t("form.web_search") + ": " + texts.t(value.webSearch ? "projects.recovery_on" : "projects.recovery_off"),
         texts.t("projects.recovery_selected_rules", ["count": value.ruleIds.count])]
        if !value.llmMode.isEmpty { lines.append(texts.t("projects.recovery_mode", ["mode": value.llmMode])) }
        return lines.joined(separator: "\n")
    }
    private var saveText: String {
        switch model.saveState {
        case .saved: texts.t("projects.saved")
        case .saving: texts.t("projects.saving")
        case .conflict: texts.t("projects.conflict")
        case .pending, .offline: texts.t("projects.unsaved")
        }
    }
    @ViewBuilder private func documents(_ project: SavedProject) -> some View {
        if project.capabilities.upload && project.archivedAt == nil {
            Button(texts.t("projects.add_documents")) {
                replacing = nil
                picking = true
            }.accessibilityIdentifier("pick-files").buttonStyle(DmButtonStyle()).disabled(
                model.busy || model.saveState == .saving)
            Text(texts.t("projects.upload_destination", ["account": accountName, "project": project.name])).font(
                .caption)
        }
        if !(project.documents ?? []).isEmpty {
            DmCard {
                Text(
                    texts.t(
                        "projects.files_ready",
                        [
                            "ready": activeDocuments.filter { $0.status == "ready" }.count,
                            "total": activeDocuments.count,
                        ])
                )
                .font(.subheadline).accessibilityAddTraits(.updatesFrequently)
                if (project.documents ?? []).contains(where: {
                    $0.removedAt == nil
                        && (["rejected", "expired", "deleted"].contains($0.status) || $0.pendingReplacementId != nil)
                }) {
                    DmNotice(message: texts.t("projects.files_attention"), error: true)
                }
                DisclosureGroup(isExpanded: $filesExpanded) {
                    ForEach((project.documents ?? []).filter { showVersions || $0.removedAt == nil }) { doc in
                        DmCard {
                            Text(doc.fileName).font(.headline)
                            Text(documentStatus(doc)).font(.subheadline)
                            if let expires = doc.expiresAt {
                                Text(texts.t("projects.expires", ["date": Format.dateTime(expires)])).font(.caption)
                            }
                            if model.canEdit && doc.removedAt == nil {
                                HStack {
                                    if doc.pendingReplacementId != nil {
                                        Button(texts.t("projects.replacement_apply")) {
                                            Task { await model.applyReplacement(doc) }
                                        }
                                        .disabled(doc.status != "ready")
                                    } else {
                                        Button(texts.t("projects.replace_document")) {
                                            replacing = doc
                                            picking = true
                                        }
                                    }
                                    Spacer()
                                    Button(texts.t("projects.remove_document"), role: .destructive) { removing = doc }
                                }.disabled(model.busy)
                            }
                        }
                    }
                    if (project.documents ?? []).contains(where: { $0.removedAt != nil }) {
                        Toggle(texts.t("projects.show_versions"), isOn: $showVersions)
                    }
                } label: {
                    Text(
                        filesExpanded
                            ? texts.t("projects.hide_files")
                            : texts.t("projects.show_files", ["count": activeDocuments.count]))
                }.accessibilityIdentifier("project-files-disclosure")
            }
        }
        Text(texts.t("projects.retention")).font(.caption).foregroundStyle(Color(DmColors.textMuted))
        if model.canEdit {
            DisclosureGroup(texts.t("projects.settings")) {
                DmCard {
                    Toggle(
                        texts.t("projects.use_rules"),
                        isOn: Binding(
                            get: { model.settings.useRules },
                            set: {
                                var s = model.settings
                                s.useRules = $0
                                model.setSettings(s)
                            }))
                    Toggle(
                        texts.t("projects.web_search"),
                        isOn: Binding(
                            get: { model.settings.webSearch },
                            set: {
                                var s = model.settings
                                s.webSearch = $0
                                model.setSettings(s)
                            }))
                    TextField(
                        texts.t("projects.rules"),
                        text: Binding(
                            get: { model.settings.rulesText },
                            set: {
                                var s = model.settings
                                s.rulesText = $0
                                model.setSettings(s)
                            }), axis: .vertical
                    ).lineLimit(3...8).dmField()
                }.disabled(model.busy)
                ProjectRulesView(model: model)
            }
            if model.analysis?.state == "expired" {
                DmNotice(message: texts.t("projects.expired_result"))
                TextField(texts.t("projects.reprocess_reason"), text: $model.reprocessReason).dmField()
            }

        }
    }
    private var activeDocuments: [ProjectDocument] {
        (model.project?.documents ?? []).filter { $0.removedAt == nil && $0.pendingReplacementId == nil }
    }
    private var results: some View {
        VStack(alignment: .leading, spacing: 12) {
            if model.runs.isEmpty { Text(texts.t("projects.no_results")) }
            if model.nextRuns != nil { Button(texts.t("projects.more")) { Task { await model.moreRuns() } } }
            ForEach(model.runs) { run in
                Button {
                    result = run.id
                } label: {
                    DmCard {
                        Text(Labels.runStatus(texts, run.status))
                        Text(Format.dateTime(run.createdAt)).font(.caption)
                    }
                }.buttonStyle(.plain)
            }
        }
    }
    private func documentStatus(_ doc: ProjectDocument) -> String {
        if doc.removedAt != nil { return texts.t("projects.document_removed") }
        if doc.pendingReplacementId != nil { return texts.t("projects.replacement_pending") }
        let status =
            ["ready", "scanning", "rejected", "expired"].contains(doc.status)
            ? doc.status : doc.status == "deleted" ? "expired" : "pending"
        return texts.t("projects.document_" + status)
    }
    private func eventLabel(_ action: String) -> String {
        let labels = [
            "project.created": "event_created", "project.updated": "event_updated",
            "project.sharing_changed": "event_shared", "document.added": "event_document",
            "document.removed": "event_removed", "document.replaced": "event_replaced", "run.created": "event_run",
        ]
        return texts.t("projects." + (labels[action] ?? "activity"))
    }
}
