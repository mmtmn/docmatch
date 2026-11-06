import DocmatchKit
import SwiftUI

/// The same account page as the site's /conta: profile, credits and every signed-in device, any of
/// which can be signed out from here; reloaded every time the tab is shown.
struct AccountView: View {
    let model: AccountModel
    @State private var confirmingDeletion = false
    @Environment(\.texts) private var texts

    init(model: AccountModel) { self.model = model }

    var body: some View {
        // The ZStack owns the task (on a Group it would run for each state that appears).
        ZStack {
            switch model.state {
            case .loading:
                LoadingView(label: texts.t("account.loading"))
            case .failed(let message):
                ErrorView(message: message) { Task { await model.refresh() } }
            case .ready(let me, let devices, let notice):
                List {
                    if let notice {
                        Text(notice)
                    }
                    Section {
                        if !me.user.displayName.isEmpty {
                            LabeledContent(texts.t("account.name_label"), value: me.user.displayName)
                        }
                        LabeledContent(texts.t("account.email_label"), value: me.user.email)
                        if let credits = me.credits {
                            LabeledContent(texts.t("account.credits"), value: credits.tokenExempt ? texts.t("account.token_exempt") : texts.t("account.credits_available", ["available": credits.available]))
                        }
                        Button(texts.t("account.sign_out")) { Task { await model.signOut() } }
                    } header: {
                        Text(texts.t("account.profile")).accessibilityAddTraits(.isHeader)
                    }
                    Section {
                        ForEach(devices) { device in
                            HStack {
                                VStack(alignment: .leading) {
                                    Text(device.name).bold()
                                    if device.current {
                                        Text(texts.t("account.this_device"))
                                            .foregroundStyle(Color(DmColors.brandPrimary))
                                    }
                                    Text(texts.t("account.last_seen", ["when": Format.dateTime(device.lastSeenAt)]))
                                        .font(.caption)
                                }
                                .accessibilityElement(children: .combine)
                                Spacer()
                                Button(texts.t("account.revoke")) { Task { await model.revoke(device) } }
                                    .buttonStyle(.bordered)
                                    .accessibilityLabel(texts.t("account.revoke_label", ["name": device.name]))
                            }
                        }
                    } header: {
                        Text(texts.t("account.devices")).accessibilityAddTraits(.isHeader)
                    } footer: {
                        Text(texts.t("account.devices_text"))
                    }
                    Section {
                        // The stores' rule: an app that creates accounts lets them be deleted in it.
                        Button(texts.t("mobile.delete_account"), role: .destructive) { confirmingDeletion = true }
                    }
                }
            }
        }
        .navigationTitle(texts.t("account.title"))
        .task { await model.refresh() }
        .alert(texts.t("mobile.delete_title"), isPresented: $confirmingDeletion) {
            Button(texts.t("mobile.delete_confirm"), role: .destructive) { Task { await model.deleteAccount() } }
            Button(texts.t("common.cancel"), role: .cancel) {}
        } message: {
            Text(texts.t("mobile.delete_text"))
        }
    }
}
