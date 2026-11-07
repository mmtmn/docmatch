// A new reconciliation: the picked files through the site's precheck (same messages), the run
// options, the upload → scan → estimate steps, the token confirmation and the start.
import Foundation
import Observation

/// A file the system picker returned: its name, its size (-1 when unknown), how to read its head,
/// and where its bytes are for the upload.
public struct PickedFile: Sendable {
    public let name: String
    public let size: Int64
    public let head: @Sendable () throws -> Data
    public let source: LocalFile.Source

    public init(name: String, size: Int64, source: LocalFile.Source, head: @escaping @Sendable () throws -> Data) {
        self.name = name
        self.size = size
        self.source = source
        self.head = head
    }
}

public enum NewRunPhase: Sendable, Equatable {
    case idle
    case working(Step)
    case confirm(Prepared)
}

@MainActor
@Observable
public final class NewRunModel {
    public private(set) var files: [LocalFile] = []
    public private(set) var options = RunOptions()
    public private(set) var phase: NewRunPhase = .idle
    public private(set) var error: String?
    /// The run just started, for the screen to open once.
    public private(set) var started: String?
    @ObservationIgnored private let dm: Docmatch

    public init(dm: Docmatch) { self.dm = dm }

    public var busy: Bool { phase != .idle }
    public var totalBytes: Int64 { files.reduce(0) { $0 + $1.size } }

    /// Adds what the picker returned, refusing what the site would refuse (same messages).
    public func add(_ picked: [PickedFile]) async {
        let (accepted, problems) = await Self.checkedFiles(picked, texts: dm.texts, existing: files)
        files += accepted
        error = problems.isEmpty ? nil : problems.joined(separator: "\n")
    }

    /// The same file precheck is used by quick uploads and saved projects.
    public static func checkedFiles(_ picked: [PickedFile], texts: Texts, existing: [LocalFile] = []) async -> ([LocalFile], [String]) {
        return await Task.detached { () -> ([LocalFile], [String]) in
            let candidates = picked.map { p in Candidate(name: p.name, size: p.size, head: p.head) }
            let (ok, refused) = Precheck.check(texts, candidates, existing: existing)
            let files = ok.compactMap { a -> LocalFile? in
                guard let p = picked.first(where: { $0.name == a.name && $0.size == a.size }) else { return nil }
                return LocalFile(name: a.name, mime: a.mime, size: a.size, kind: a.kind, source: p.source)
            }
            return (files, refused)
        }.value
    }

    public func remove(_ file: LocalFile) {
        guard !busy else { return }
        files.removeAll { $0.id == file.id }
    }

    /// The rules text, cut at the limit the API enforces (code points after NFC).
    public func setRules(_ value: String) {
        options.rulesText = Self.cut(value, Limits.Text.rulesText)
    }

    public func setUseRules(_ on: Bool) { options.useRules = on }

    public func setWebSearch(_ on: Bool) { options.webSearch = on }

    /// Uploads, scans and asks for the estimate; the user then confirms the tokens.
    public func submit() async {
        guard !files.isEmpty, phase == .idle else { return }
        error = nil
        phase = .working(.uploading(done: 0, total: files.count))
        do {
            let prepared = try await dm.reconciler.prepare(files, options: options) { step in
                Task { @MainActor [weak self] in self?.progress(step) }
            }
            phase = .confirm(prepared)
        } catch {
            phase = .idle
            self.error = message(error)
        }
    }

    public func cancel() {
        guard case .confirm = phase else { return }
        phase = .idle
        error = dm.texts.t("app.cancelled")
    }

    /// Starts the accepted run. A lost answer or a server error keeps the confirmation open: the
    /// next tap sends the same estimate with the same key, so it never starts a second run.
    public func confirm() async {
        guard case .confirm(let prepared) = phase else { return }
        phase = .working(.starting)
        do {
            let run = try await dm.reconciler.start(prepared)
            files = []
            options = RunOptions()
            phase = .idle
            error = nil
            started = run.id
        } catch {
            let status = (error as? ApiError)?.status ?? 0
            phase = status == 0 || status >= 500 ? .confirm(prepared) : .idle
            self.error = message(error)
        }
    }

    public func startedShown() { started = nil }

    /// Code points after NFC, how limits.yaml counts.
    public nonisolated static func count(_ s: String) -> Int { s.precomposedStringWithCanonicalMapping.unicodeScalars.count }

    /// A value cut at a limit in code points after NFC.
    public nonisolated static func cut(_ value: String, _ max: Int) -> String {
        let scalars = value.precomposedStringWithCanonicalMapping.unicodeScalars
        return scalars.count <= max ? value : String(String.UnicodeScalarView(scalars.prefix(max)))
    }

    private func progress(_ step: Step) {
        if case .working = phase { phase = .working(step) }
    }

    private func message(_ error: any Error) -> String { (error as? ApiError)?.message ?? dm.texts.t("errors.unknown") }
}
