import DocmatchKit
import SwiftUI

struct InvitationInboxView: View {
    let session: SessionModel
    @State private var model: InvitationInboxModel
    @State private var pending: InvitationPreview?
    @State private var accepting = false
    @Environment(\.texts) private var texts
    init(session: SessionModel) {
        self.session = session
        _model = State(initialValue: InvitationInboxModel(dm: session.dm))
    }
    var body: some View {
        DmPage {
            DmHeading(title: texts.t("teams.inbox_title"), subtitle: texts.t("teams.inbox_hint"))
            Text(texts.t("teams.personal_separate"))
            if let notice = model.notice { DmNotice(message: notice) }
            if let error = model.error {
                DmNotice(message: error, error: true)
                Button(texts.t("projects.retry_load")) { Task { await model.load() } }
            }
            if model.busy { ProgressView() }
            if model.items.isEmpty && !model.busy && model.error == nil {
                Text(texts.t("teams.inbox_empty"))
                Text(texts.t("teams.inbox_verify"))
            }
            ForEach(model.items) { invite in
                DmCard {
                    Text(invite.organization).font(.title2.bold())
                    Text(Labels.accountRole(texts, invite.role)).font(.headline)
                    Text(texts.t(invite.role == "uploader" ? "teams.upload_explanation" : "teams.full_explanation"))
                    Text(texts.t("teams.expires", ["date": Format.dateTime(invite.expiresAt)]))
                    Button(texts.t("teams.accept_invitation")) { accepting = true; pending = invite }.buttonStyle(DmButtonStyle())
                    Button(texts.t("teams.decline_invitation"), role: .destructive) { accepting = false; pending = invite }
                }
            }
            if model.nextCursor != nil {
                Button(texts.t("teams.inbox_more")) { Task { await model.load(more: true) } }
            }
        }
        .disabled(model.busy)
        .dmNavigation(texts.t("teams.inbox_title"))
        .task { await model.load() }
        .refreshable { await model.load() }
        .confirmationDialog(
            texts.t(accepting ? "teams.inbox_accept" : "teams.inbox_decline", ["name": pending?.organization ?? ""]),
            isPresented: Binding(get: { pending != nil }, set: { if !$0 { pending = nil } }), titleVisibility: .visible
        ) {
            Button(texts.t("common.confirm")) {
                if let invite = pending {
                    let accept = accepting
                    Task {
                        if await model.respond(invite.id, accept: accept) { await session.validateAccounts() }
                    }
                }
                pending = nil
            }
        }
    }
}
