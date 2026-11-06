import DocmatchKit
import SwiftUI

struct TeamView: View {
    let session: SessionModel
    @State private var model: TeamModel
    @State private var name = ""
    @State private var email = ""
    @State private var role = "uploader"
    @State private var pending: TeamAction?
    @State private var confirmation = ""
    @Environment(\.texts) private var texts
    @Environment(\.dismiss) private var dismiss
    init(session: SessionModel) {
        self.session = session
        _model = State(initialValue: TeamModel(dm: session.currentDM))
    }
    var body: some View {
        DmPage {
            DmHeading(title: texts.t("teams.title"), subtitle: texts.t("teams.no_charge"))
            if let team = model.team {
                Text(team.name).font(.title2.bold())
                if team.kind == "personal" && team.capabilities.manageAccount {
                    DmCard {
                        Text(texts.t("teams.convert_title")).font(.headline)
                        Text(texts.t("teams.convert_body"))
                        TextField(texts.t("teams.name"), text: $name).dmField()
                        Button(texts.t("teams.convert")) {
                            ask(.convert(name), texts.t("teams.convert_confirm", ["name": name]))
                        }.buttonStyle(DmButtonStyle()).disabled(
                            name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                } else if team.kind == "team" {
                    if team.capabilities.manageAccount { management(team) }
                    if team.role != "owner" {
                        DmCard {
                            Text(texts.t("teams.leave_explanation"))
                            Button(texts.t("teams.leave"), role: .destructive) {
                                ask(.leave, texts.t("teams.leave_confirm", ["name": team.name]))
                            }
                        }
                    }
                }
            }
            if model.busy { ProgressView() }
            if let message = model.notice { DmNotice(message: message) }
            if let message = model.error { DmNotice(message: message, error: true) }
        }
        .disabled(model.busy)
        .dmNavigation(texts.t("teams.title"))
        .task { await model.load() }
        .refreshable { await model.load() }
        .confirmationDialog(
            confirmation, isPresented: Binding(get: { pending != nil }, set: { if !$0 { pending = nil } }),
            titleVisibility: .visible
        ) {
            Button(texts.t("common.confirm")) {
                if let action = pending {
                    Task {
                        await model.perform(action)
                        await session.validateAccounts()
                        if model.left { dismiss() }
                    }
                }
                pending = nil
            }
        }
    }
    private func ask(_ action: TeamAction, _ message: String) {
        pending = action
        confirmation = message
    }
    @ViewBuilder private func management(_ team: TeamInfo) -> some View {
        DmCard {
            Text(texts.t("teams.invite")).font(.headline)
            TextField(texts.t("teams.email"), text: $email).keyboardType(.emailAddress).textInputAutocapitalization(
                .never
            ).autocorrectionDisabled().dmField()
            Picker(texts.t("teams.permission"), selection: $role) {
                Text(texts.t("teams.upload_only")).tag("uploader")
                Text(texts.t("teams.full_access")).tag("reconciler")
            }
            Text(texts.t(role == "uploader" ? "teams.upload_explanation" : "teams.full_explanation"))
            Text(texts.t("teams.accept_required")).font(.caption)
            Button(texts.t("teams.send_invite")) {
                Task {
                    await model.perform(.invite(email.trimmingCharacters(in: .whitespacesAndNewlines), role))
                    if model.error == nil { email = "" }
                }
            }.buttonStyle(DmButtonStyle()).disabled(!team.canInvite || !email.contains("@"))
            if !team.canInvite { Text(texts.t("teams.subscription_required")) }
            Text(
                texts.t(
                    "teams.members_limit",
                    [
                        "count": team.members.count + team.invitations.filter { $0.status == "pending" }.count,
                        "max": team.memberLimit,
                    ])
            ).font(.caption)
        }
        Text(texts.t("teams.sharing_reminder")).font(.subheadline)
        Text(texts.t("teams.members")).font(.headline)
        ForEach(team.members) { member in
            DmCard {
                Text(member.email).font(.headline)
                Text(Labels.accountRole(texts, member.role))
                if ["uploader", "reconciler"].contains(member.role) {
                    let next = member.role == "uploader" ? "reconciler" : "uploader"
                    Button(Labels.accountRole(texts, next)) {
                        ask(
                            .role(member.id, next),
                            texts.t(
                                "teams.change_role_confirm",
                                ["email": member.email, "role": Labels.accountRole(texts, next)]))
                    }
                    Button(texts.t("teams.remove"), role: .destructive) {
                        ask(.remove(member.id), texts.t("teams.remove_confirm", ["email": member.email]))
                    }
                    if team.role == "owner" && member.role == "reconciler" {
                        Button(texts.t("teams.transfer")) {
                            ask(.transfer(member.id), texts.t("teams.transfer_confirm", ["email": member.email]))
                        }
                    }
                }
            }
        }
        Text(texts.t("teams.invitations")).font(.headline)
        if team.invitations.isEmpty { Text(texts.t("teams.no_invitations")) }
        ForEach(team.invitations) { invite in
            DmCard {
                Text(invite.email).font(.headline)
                Text(texts.t("teams." + invite.status) + " · " + Labels.accountRole(texts, invite.role))
                Text(texts.t("teams.expires", ["date": Format.dateTime(invite.expiresAt)])).font(.caption)
                if ["pending", "expired"].contains(invite.status) {
                    Button(texts.t("teams.resend")) { Task { await model.perform(.resend(invite.id)) } }
                    Button(texts.t("teams.revoke"), role: .destructive) {
                        ask(.revoke(invite.id), texts.t("teams.revoke_confirm", ["email": invite.email]))
                    }
                }
            }
        }
    }
}

struct InvitationView: View {
    let session: SessionModel
    @State private var model: InvitationModel
    @Environment(\.texts) private var texts
    @Environment(\.dismiss) private var dismiss
    init(session: SessionModel, token: String) {
        self.session = session
        _model = State(initialValue: InvitationModel(dm: session.dm, token: token))
    }
    var body: some View {
        NavigationStack {
            DmPage {
                DmHeading(title: texts.t("teams.invitation_title"), subtitle: texts.t("teams.personal_separate"))
                if let invite = model.preview {
                    Text(invite.organization).font(.title2.bold())
                    Text(texts.t("teams.invited_email", ["email": invite.emailHint]))
                    Text(Labels.accountRole(texts, invite.role)).font(.headline)
                    Text(texts.t(invite.role == "uploader" ? "teams.upload_explanation" : "teams.full_explanation"))
                    Text(texts.t("teams.expires", ["date": Format.dateTime(invite.expiresAt)]))
                    if let response = model.response {
                        if response.accepted {
                            Button(texts.t("teams.open_team")) {
                                Task {
                                    await session.validateAccounts()
                                    await session.selectAccount(response.organizationId)
                                    dismiss()
                                }
                            }.buttonStyle(DmButtonStyle())
                        } else {
                            Text(texts.t("teams.declined_message"))
                        }
                    } else if !["pending", "accepted"].contains(invite.status) {
                        DmNotice(message: texts.t("teams." + invite.status))
                    } else if case .signedIn = session.state {
                        if invite.emailMatches {
                            Button(texts.t(invite.status == "accepted" ? "teams.open_team" : "teams.accept_invitation"))
                            {
                                Task {
                                    await model.respond(accept: true)
                                    if invite.status == "accepted", let response = model.response, response.accepted {
                                        await session.selectAccount(response.organizationId)
                                        dismiss()
                                    }
                                }
                            }.buttonStyle(DmButtonStyle())
                            if invite.status == "pending" {
                                Button(texts.t("teams.decline_invitation"), role: .destructive) {
                                    Task { await model.respond(accept: false) }
                                }
                            }
                        } else {
                            DmNotice(message: texts.t("teams.invite_email_mismatch"))
                            Button(texts.t("teams.switch_login")) {
                                Task {
                                    try? await session.dm.api.logout()
                                    await session.signedOut(notice: nil)
                                }
                            }
                        }
                    }
                }
                if case .signedOut(let state) = session.state {
                    Text(texts.t("teams.sign_in_invited"))
                    LoginView(state: state, session: session)
                }
                if let error = model.error { DmNotice(message: error, error: true) }
                if model.busy { ProgressView() }
            }
            .disabled(model.busy)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button(texts.t("common.cancel")) { dismiss() } } }
            .task { await model.load() }
            .onChange(of: session.state) { _, _ in Task { await model.load() } }
        }
    }
}
