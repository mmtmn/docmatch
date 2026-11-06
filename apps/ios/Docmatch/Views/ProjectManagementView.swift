import DocmatchKit
import SwiftUI

struct ProjectSharingView: View {
    let model: ProjectModel
    @State private var members: [TeamMember] = []
    @State private var selected: Set<String> = []
    @State private var visibility = "private"
    @State private var error: String?
    @State private var confirming = false
    @Environment(\.texts) private var texts
    var body: some View {
        DisclosureGroup(texts.t("projects.sharing")) {
            VStack(alignment: .leading, spacing: 14) {
                Text(texts.t("projects.sharing_explanation"))
                Picker(texts.t("projects.visibility"), selection: $visibility) {
                    Text(texts.t("projects.visibility_selected")).tag("private")
                    Text(texts.t("projects.visibility_team")).tag("team")
                }
                if visibility == "private" {
                    ForEach(members) { member in
                        Toggle(
                            member.email + " · " + Labels.accountRole(texts, member.role),
                            isOn: Binding(
                                get: { selected.contains(member.id) },
                                set: { if $0 { selected.insert(member.id) } else { selected.remove(member.id) } }))
                    }
                }
                Button(texts.t("projects.sharing_save")) { confirming = true }.buttonStyle(DmButtonStyle(.secondary))
                if let error { DmNotice(message: error, error: true) }
            }.padding(.top, 12).disabled(model.busy)
        }
        .task {
            visibility = model.project?.visibility ?? "private"
            selected = Set(model.project?.sharedWith ?? [])
            do {
                members = try await model.dm.api.team().members.filter { !["owner", "admin"].contains($0.role) }
            } catch { self.error = (error as? ApiError)?.message ?? texts.t("errors.unknown") }
        }
        .confirmationDialog(
            texts.t(visibility == "team" ? "projects.share_confirm" : "projects.sharing_confirm"),
            isPresented: $confirming, titleVisibility: .visible
        ) {
            Button(texts.t("common.confirm")) {
                Task { await model.share(visibility: visibility, users: Array(selected).sorted()) }
            }
        }
    }
}

struct ProjectRulesView: View {
    let model: ProjectModel
    @State private var rules: [ProjectRule] = []
    @State private var next: String?
    @State private var busy = false
    @State private var error: String?
    @State private var editing: ProjectRule?
    @State private var showEditor = false
    @State private var promoting: ProjectRule?
    @Environment(\.texts) private var texts
    var body: some View {
        DisclosureGroup(texts.t("projects.saved_rules")) {
            VStack(alignment: .leading, spacing: 16) {
                Text(texts.t("projects.rules_review_explanation"))
                if rules.isEmpty { Text(texts.t("projects.no_saved_rules")) }
                ForEach(rules) { rule in ruleRow(rule) }
                if next != nil { Button(texts.t("projects.more")) { Task { await load(more: true) } } }
                Button(texts.t("projects.rule_new")) {
                    editing = nil
                    showEditor = true
                }
                if busy { ProgressView() }
                if let error { DmNotice(message: error, error: true) }
            }.padding(.top, 12).disabled(busy || model.busy)
        }
        .task { await load() }
        .sheet(isPresented: $showEditor) {
            ProjectRuleEditor(model: model, initial: editing) { value in
                if let index = rules.firstIndex(where: { $0.id == value.id }) {
                    rules[index] = value
                } else {
                    rules.insert(value, at: 0)
                }
            }
        }
        .confirmationDialog(
            texts.t(
                promoting?.status == "candidate" ? "projects.rule_verify_confirm" : "projects.rule_trust_confirm",
                ["title": promoting?.title ?? ""]),
            isPresented: Binding(get: { promoting != nil }, set: { if !$0 { promoting = nil } }),
            titleVisibility: .visible
        ) {
            Button(texts.t("common.confirm")) {
                if let rule = promoting {
                    Task { await promote(rule, to: rule.status == "candidate" ? "verified" : "trusted") }
                }
                promoting = nil
            }
        }
    }
    @ViewBuilder private func ruleRow(_ rule: ProjectRule) -> some View {
        DmCard {
            Text(rule.title).font(.headline)
            Text(
                texts.t("projects.rule_" + rule.status) + " · "
                    + texts.t("projects.rule_version", ["version": rule.version])
            ).font(.caption)
            Text(texts.t(rule.provenance.origin == "user" ? "projects.rule_by_person" : "projects.rule_by_analysis"))
                .font(.caption)
            Text(rule.body).textSelection(.enabled)
            Toggle(
                texts.t("projects.rule_select"),
                isOn: Binding(
                    get: { model.settings.ruleIds.contains(rule.id) },
                    set: { selected in
                        var settings = model.settings
                        settings.ruleIds =
                            selected ? settings.ruleIds + [rule.id] : settings.ruleIds.filter { $0 != rule.id }
                        model.setSettings(settings)
                    })
            ).disabled(rule.status == "retired")
            if rule.status != "retired" {
                Button(texts.t("projects.rule_save_version")) {
                    editing = rule
                    showEditor = true
                }
                if ["candidate", "verified"].contains(rule.status) {
                    Button(texts.t(rule.status == "candidate" ? "projects.rule_verify" : "projects.rule_trust")) {
                        promoting = rule
                    }
                }
                Button(texts.t("projects.rule_retire"), role: .destructive) {
                    Task { await promote(rule, to: "retired") }
                }
            }
        }
    }
    private func load(more: Bool = false) async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let page = try await model.dm.api.projectRules(model.id, cursor: more ? next : nil)
            rules = more ? rules + page.items.filter { r in !rules.contains { $0.id == r.id } } : page.items
            next = page.nextCursor
        } catch { self.error = (error as? ApiError)?.message ?? texts.t("errors.unknown") }
    }
    private func promote(_ rule: ProjectRule, to: String) async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let saved = try await model.dm.api.promoteProjectRule(
                model.id, rule: rule.id, to: to, idempotencyKey: UUID().uuidString)
            if let index = rules.firstIndex(where: { $0.id == saved.id }) { rules[index] = saved }
            if to == "retired" {
                var settings = model.settings
                settings.ruleIds.removeAll { $0 == rule.id }
                model.setSettings(settings)
            }
        } catch { self.error = (error as? ApiError)?.message ?? texts.t("errors.unknown") }
    }
}

private struct ProjectRuleEditor: View {
    let model: ProjectModel
    let initial: ProjectRule?
    let onSave: (ProjectRule) -> Void
    @State private var title = ""
    @State private var bodyText = ""
    @State private var key = UUID().uuidString
    @State private var busy = false
    @State private var error: String?
    @Environment(\.texts) private var texts
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        DmPage {
            DmHeading(
                title: texts.t(initial == nil ? "projects.rule_new" : "projects.rule_save_version"),
                subtitle: texts.t("projects.rules_review_explanation"))
            TextField(texts.t("projects.rule_title"), text: $title).dmField()
            TextField(texts.t("projects.rule_body"), text: $bodyText, axis: .vertical).lineLimit(4...10).dmField()
            Button(texts.t("projects.rule_save")) { Task { await save() } }.buttonStyle(DmButtonStyle()).disabled(
                title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                    || bodyText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            if let error { DmNotice(message: error, error: true) }
            Button(texts.t("common.cancel")) { dismiss() }
        }
        .disabled(busy).interactiveDismissDisabled(busy)
        .onAppear {
            title = initial?.title ?? ""
            bodyText = initial?.body ?? ""
        }
        .onChange(of: title) { _, _ in key = UUID().uuidString }
        .onChange(of: bodyText) { _, _ in key = UUID().uuidString }
    }
    private func save() async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let value = try await model.dm.api.saveProjectRule(
                model.id, rule: initial?.id, title: title, body: bodyText, idempotencyKey: key)
            onSave(value)
            dismiss()
        } catch { self.error = (error as? ApiError)?.message ?? texts.t("errors.unknown") }
    }
}
