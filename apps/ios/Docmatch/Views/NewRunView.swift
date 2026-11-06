import DocmatchKit
import SwiftUI
import UniformTypeIdentifiers

/// A new reconciliation: the files from the system picker, the rules and options of the site's
/// form, the estimate to confirm, and the run.
struct NewRunView: View {
    var tokenExempt = false
    let model: NewRunModel
    let onStarted: (String) -> Void
    @State private var picking = false
    @Environment(\.texts) private var texts

    init(model: NewRunModel, tokenExempt: Bool = false, onStarted: @escaping (String) -> Void) {
        self.tokenExempt = tokenExempt
        self.model = model
        self.onStarted = onStarted
    }

    var body: some View {
        Form {
            Section {
                Text(texts.t("mobile.new_text"))
                    .foregroundStyle(.secondary)
                Button(texts.t("mobile.pick_files")) { picking = true }
                    .disabled(model.busy)
            }

            if !model.files.isEmpty {
                Section(Labels.files(texts, count: model.files.count, bytes: model.totalBytes)) {
                    ForEach(model.files) { file in
                        HStack {
                            VStack(alignment: .leading) {
                                Text(file.name)
                                Text(Labels.kind(texts, file.kind) + " · " + Format.size(texts, file.size))
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            .accessibilityElement(children: .combine)
                            Spacer()
                            Button {
                                model.remove(file)
                            } label: {
                                Image(systemName: "xmark.circle.fill")
                            }
                            .buttonStyle(.borderless)
                            .accessibilityLabel(texts.t("dropzone.remove_file", ["name": file.name]))
                            .disabled(model.busy)
                        }
                    }
                }
            }

            Section {
                TextField(
                    texts.t("form.rules_label"),
                    text: Binding(get: { model.options.rulesText }, set: { model.setRules($0) }),
                    prompt: Text(texts.t("form.rules_placeholder")),
                    axis: .vertical
                )
                .lineLimit(3...8)
                .disabled(model.busy)
                CounterText(count: NewRunModel.count(model.options.rulesText), max: Limits.Text.rulesText)
                Text(texts.t("form.rules_help"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                Toggle(isOn: Binding(get: { model.options.useRules }, set: { model.setUseRules($0) })) {
                    option(texts.t("form.use_rules"), texts.t("tooltips.use_rules"))
                }
                Toggle(isOn: Binding(get: { model.options.webSearch }, set: { model.setWebSearch($0) })) {
                    option(texts.t("form.web_search"), texts.t("tooltips.web_search"))
                }
            }
            .disabled(model.busy)

            Section {
                Button(model.busy ? texts.t("form.submitting") : texts.t("form.submit")) {
                    Task { await model.submit() }
                }
                .disabled(model.busy || model.files.isEmpty)
                if case .working(let step) = model.phase {
                    ProgressView()
                    Text(Labels.step(texts, step))
                }
                if let error = model.error {
                    Text(error)
                        .foregroundStyle(Color(DmColors.statusDanger))
                }
            }
        }
        .navigationTitle(texts.t("common.app_title"))
        .fileImporter(isPresented: $picking, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            Task { await model.add(urls.map { Self.picked($0) }) }
        }
        .alert(texts.t("mobile.confirm_title"), isPresented: confirming) {
            Button(texts.t("form.submit")) { Task { await model.confirm() } }
            Button(texts.t("common.cancel"), role: .cancel) { model.cancel() }
        } message: {
            if case .confirm(let prepared) = model.phase {
                // After a failed start the same estimate is offered again, with the reason.
                Text((tokenExempt ? texts.t("app.confirm_exempt") : Format.confirmRun(texts, prepared.quote)) + "\n\n" + texts.t("app.confirm_scope") + (model.error.map { "\n\n" + $0 } ?? ""))
            }
        }
        .onChange(of: model.started) { _, runId in
            guard let runId else { return }
            model.startedShown()
            onStarted(runId)
        }
    }

    private var confirming: Binding<Bool> {
        Binding(
            get: {
                if case .confirm = model.phase { return true }
                return false
            },
            set: { _ in })
    }

    private func option(_ label: String, _ help: String) -> some View {
        VStack(alignment: .leading) {
            Text(label)
            Text(help)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
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
