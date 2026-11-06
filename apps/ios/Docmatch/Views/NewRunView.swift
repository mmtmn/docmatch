import DocmatchKit
import SwiftUI
import UniformTypeIdentifiers

struct NewRunView: View {
    var tokenExempt = false
    let model: NewRunModel
    let onStarted: (String) -> Void
    @State private var picking = false
    @Environment(\.texts) private var texts

    init(model: NewRunModel, tokenExempt: Bool = false, onStarted: @escaping (String) -> Void) {
        self.model = model; self.tokenExempt = tokenExempt; self.onStarted = onStarted
    }

    var body: some View {
        DmPage {
            if case .working(let step) = model.phase {
                DmProcessingScene(label: Labels.step(texts, step))
            } else {
                DmHeading(title: texts.t("landing.title"), subtitle: texts.t("landing.subtitle"), hero: true)
                DmUploadTarget(compact: !model.files.isEmpty || model.options.rulesText.count > 120) { picking = true }
                    .disabled(model.busy)
                filesSection
                rulesSection
            }
        }
        .dmNavigation(texts.t("mobile.new_title"))
        .safeAreaInset(edge: .bottom) { submitSection }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            Task { await model.add(urls.map { Self.picked($0) }) }
        }
        .sheet(isPresented: confirming) { confirmationSheet }
        .onChange(of: model.started) { _, runId in
            guard let runId else { return }
            model.startedShown(); onStarted(runId)
        }
    }

    @ViewBuilder private var filesSection: some View {
        if !model.files.isEmpty {
            DmCard {
                Text(Labels.files(texts, count: model.files.count, bytes: model.totalBytes)).font(.subheadline.bold())
                ForEach(model.files) { file in
                    HStack(spacing: 10) {
                        Image(systemName: "doc.text").foregroundStyle(Color(DmColors.brandPrimary)).accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: 4) {
                            Text(file.name).font(.subheadline).lineLimit(2)
                            Text(Labels.kind(texts, file.kind) + " · " + Format.size(texts, file.size))
                                .font(.caption).foregroundStyle(Color(DmColors.textMuted))
                        }
                        .accessibilityElement(children: .combine)
                        Spacer(minLength: 4)
                        Button { model.remove(file) } label: {
                            Image(systemName: "xmark").frame(width: 44, height: 44)
                        }
                        .buttonStyle(.plain).foregroundStyle(Color(DmColors.textMuted))
                        .accessibilityLabel(texts.t("dropzone.remove_file", ["name": file.name])).disabled(model.busy)
                    }
                }
            }
        }
    }

    private var rulesSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(texts.t("form.rules_label")).font(.headline).accessibilityAddTraits(.isHeader)
            Toggle(texts.t("form.use_rules"), isOn: Binding(get: { model.options.useRules }, set: { model.setUseRules($0) }))
                .accessibilityHint(texts.t("tooltips.use_rules"))
            Toggle(texts.t("form.web_search"), isOn: Binding(get: { model.options.webSearch }, set: { model.setWebSearch($0) }))
                .accessibilityHint(texts.t("tooltips.web_search"))
            TextField(texts.t("form.rules_label"), text: Binding(get: { model.options.rulesText }, set: { model.setRules($0) }),
                prompt: Text(texts.t("form.rules_placeholder")), axis: .vertical)
                .lineLimit(2...6).dmField().disabled(!model.options.useRules)
            CounterText(count: NewRunModel.count(model.options.rulesText), max: Limits.Text.rulesText)
            if !model.options.useRules {
                Text(texts.t("form.rules_disabled_hint")).font(.caption).foregroundStyle(Color(DmColors.textMuted))
            }
            DmDisclosure(texts.t("mobile.rules_more")) {
                Text(texts.t("tooltips.use_rules")).font(.subheadline)
                Text(texts.t("tooltips.web_search")).font(.subheadline)
            }
        }.disabled(model.busy)
    }

    private var submitSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let error = model.error { DmNotice(message: error, error: true) }
            Button(model.busy ? texts.t("form.submitting") : texts.t("form.submit")) { Task { await model.submit() } }
                .buttonStyle(DmButtonStyle()).disabled(model.busy || model.files.isEmpty)
                .accessibilityIdentifier("submit-run")
            Text(texts.t(model.files.isEmpty ? "form.disabled_no_files" : "mobile.files_private"))
                .font(.caption).foregroundStyle(Color(DmColors.textMuted))
        }
        .frame(maxWidth: 720).padding(.horizontal, 20).padding(.vertical, 12).frame(maxWidth: .infinity)
        .background(Color(DmColors.bgCanvas))
    }

    private var confirmationSheet: some View {
        DmPage {
            DmIconTile(name: "doc.text.magnifyingglass")
            DmHeading(title: texts.t("mobile.confirm_title"), subtitle: texts.t("mobile.review_before_start"))
            if case .confirm(let prepared) = model.phase {
                DmCard {
                    Text(tokenExempt ? texts.t("app.confirm_exempt") : Format.confirmRun(texts, prepared.quote)).font(.headline)
                    Text(texts.t("app.confirm_scope")).font(.subheadline).foregroundStyle(Color(DmColors.textMuted))
                    if let error = model.error { DmNotice(message: error, error: true) }
                }
            }
            Button(texts.t("form.submit")) { Task { await model.confirm() } }.buttonStyle(DmButtonStyle())
                .accessibilityIdentifier("confirm-run")
            Button(texts.t("common.cancel")) { model.cancel() }.buttonStyle(DmButtonStyle(.secondary))
        }
        .presentationDetents([.medium, .large]).presentationDragIndicator(.visible)
    }

    private var confirming: Binding<Bool> {
        Binding(get: { if case .confirm = model.phase { return true }; return false },
            set: { showing in if !showing, case .confirm = model.phase { model.cancel() } })
    }

    /// A picked file: name and size now, the first bytes for the precheck, the rest at upload.
    private static func picked(_ url: URL) -> PickedFile {
        let granted = url.startAccessingSecurityScopedResource()
        defer { if granted { url.stopAccessingSecurityScopedResource() } }
        let reported: Int? = (try? url.resourceValues(forKeys: [.fileSizeKey]))?.fileSize
        let size = reported.map { Int64($0) } ?? -1
        return PickedFile(name: url.lastPathComponent, size: size, source: .file(url, scoped: true)) {
            let access = url.startAccessingSecurityScopedResource()
            defer { if access { url.stopAccessingSecurityScopedResource() } }
            let handle = try FileHandle(forReadingFrom: url)
            defer { try? handle.close() }
            return try handle.read(upToCount: Precheck.headBytes) ?? Data()
        }
    }
}
