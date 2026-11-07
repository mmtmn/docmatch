import DocmatchKit
import SwiftUI

/// The same account page as the site's /conta: profile, credits and every signed-in device, any of
/// which can be signed out from here; reloaded every time the tab is shown.
struct AccountView: View {
    let model: AccountModel
    let canManage: Bool
    let session: SessionModel?
    @State private var confirmingDeletion = false
    @Environment(\.texts) private var texts

    init(model: AccountModel, canManage: Bool = true, session: SessionModel? = nil) { self.model = model; self.canManage = canManage; self.session = session }

    var body: some View {
        // The ZStack owns the task (on a Group it would run for each state that appears).
        ZStack {
            switch model.state {
            case .loading:
                LoadingView(label: texts.t("account.loading"))
            case .failed(let message):
                ErrorView(message: message) { Task { await model.refresh() } }
            case .ready(let me, let devices, let notice):
                DmPage {
                    DmHeading(title: texts.t("account.title"), subtitle: texts.t("mobile.account_text"))
                    if let notice { DmNotice(message: notice) }
                    DmCard {
                        DmIconTile(name: "person.crop.circle")
                        Text(texts.t("account.profile")).font(.headline).accessibilityAddTraits(.isHeader)
                        if !me.user.displayName.isEmpty { profileValue(texts.t("account.name_label"), me.user.displayName) }
                        profileValue(texts.t("account.email_label"), me.user.email)
                        if canManage, let credits = me.credits {
                            profileValue(texts.t("account.credits"), credits.tokenExempt ? texts.t("account.token_exempt") : texts.t("account.credits_available", ["available": credits.available]))
                        }
                        Button(texts.t("account.sign_out")) { Task { await model.signOut() } }.buttonStyle(DmButtonStyle(.secondary))
                    }
                    if let session, session.currentAccount != nil {
                        NavigationLink { TeamView(session: session) } label: { Label(texts.t("teams.title"), systemImage: "person.2") }
                    }
                    DmHeading(title: texts.t("account.devices"), subtitle: texts.t("account.devices_text"))
                    ForEach(devices) { device in
                        DmCard {
                            HStack(spacing: 12) {
                                Image(systemName: device.current ? "iphone" : "desktopcomputer").foregroundStyle(Color(DmColors.textMuted)).accessibilityHidden(true)
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(device.name).font(.headline)
                                    if device.current { Text(texts.t("account.this_device")).font(.caption).foregroundStyle(Color(DmColors.brandPrimary)) }
                                    Text(texts.t("account.last_seen", ["when": Format.dateTime(device.lastSeenAt)])).font(.caption).foregroundStyle(Color(DmColors.textMuted))
                                }
                            }.accessibilityElement(children: .combine)
                            Button(texts.t("account.revoke")) { Task { await model.revoke(device) } }.buttonStyle(DmButtonStyle(.secondary))
                                .accessibilityLabel(texts.t("account.revoke_label", ["name": device.name]))
                        }.accessibilityIdentifier("device-row")
                    }
                    if canManage { Button(texts.t("mobile.delete_account"), role: .destructive) { confirmingDeletion = true }.buttonStyle(DmButtonStyle(.destructive)) }
                }
            }
        }
        .dmNavigation(texts.t("account.title"))
        .task { await model.refresh() }
        .sheet(isPresented: $confirmingDeletion) {
            DmPage {
                DmHeading(title: texts.t("mobile.delete_title"), subtitle: texts.t("mobile.delete_text"))
                Text(texts.t("mobile.apple_delete_help")).font(.footnote).foregroundStyle(.secondary)
                Link(texts.t("mobile.apple_delete_link"), destination: URL(string: "https://support.apple.com/en-us/102571")!)
                Button(texts.t("mobile.delete_confirm"), role: .destructive) {
                    confirmingDeletion = false
                    Task { await model.deleteAccount() }
                }.buttonStyle(DmButtonStyle(.destructive))
                Button(texts.t("common.cancel")) { confirmingDeletion = false }.buttonStyle(DmButtonStyle(.secondary))
            }.presentationDetents([.medium, .large]).presentationDragIndicator(.visible)
        }
    }

    private func profileValue(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.caption).foregroundStyle(Color(DmColors.textMuted))
            Text(value).font(.body).textSelection(.enabled)
        }.accessibilityElement(children: .combine)
    }
}
