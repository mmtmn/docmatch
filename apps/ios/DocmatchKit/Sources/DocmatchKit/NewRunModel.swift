// A new reconciliation: the picked files (refused with the site's messages), the rules, the
// upload → scan → estimate steps, the token confirmation and the start.
import Foundation
import Observation

/// A file the system picker returned: its name and size, and how to read it once it passes.
public struct PickedFile: Sendable {
    public let name: String
    public let size: Int64
    public let read: @Sendable () throws -> Data

    public init(name: String, size: Int64, read: @escaping @Sendable () throws -> Data) {
        self.name = name
        self.size = size
        self.read = read
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
    public private(set) var rules = ""
    public private(set) var phase: NewRunPhase = .idle
    public private(set) var error: String?
    @ObservationIgnored private let dm: Docmatch

    public init(dm: Docmatch) { self.dm = dm }

    public var busy: Bool { phase != .idle }
    public var totalBytes: Int64 { files.reduce(0) { $0 + Int64($1.bytes.count) } }

    /// Adds what the picker returned, refusing what the site would refuse (same messages).
    public func add(_ picked: [PickedFile]) async {
        let texts = dm.texts
        let current = files
        let (accepted, problems) = await Task.detached { () -> ([LocalFile], [String]) in
            var accepted: [LocalFile] = []
            var problems: [String] = []
            for p in picked {
                if let problem = FileTypes.problem(texts, name: p.name, size: p.size) {
                    problems.append(problem)
                } else if current.contains(where: { $0.name == p.name }) || accepted.contains(where: { $0.name == p.name }) {
                    problems.append(texts.t("errors.file_duplicate", ["name": p.name]))
                } else if let bytes = try? p.read() {
                    accepted.append(LocalFile(name: p.name, mime: FileTypes.mime(for: p.name)!, bytes: bytes))
                } else {
                    problems.append(texts.t("mobile.file_unreadable", ["name": p.name]))
                }
            }
            return (accepted, problems)
        }.value
        files += accepted
        error = problems.isEmpty ? nil : problems.joined(separator: "\n")
    }

    public func remove(_ name: String) {
        guard !busy else { return }
        files.removeAll { $0.name == name }
    }

    /// The rules text, cut at the limit the API enforces (Unicode scalars after NFC).
    public func setRules(_ value: String) {
        let scalars = value.precomposedStringWithCanonicalMapping.unicodeScalars
        rules = scalars.count <= Limits.Text.rulesText ? value : String(String.UnicodeScalarView(scalars.prefix(Limits.Text.rulesText)))
    }

    /// Uploads, scans and asks for the estimate; the user then confirms the tokens.
    public func submit() async {
        guard !files.isEmpty, phase == .idle else { return }
        error = nil
        phase = .working(.uploading(done: 0, total: files.count))
        do {
            let prepared = try await dm.reconciler.prepare(files, rulesText: rules) { step in
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

    /// Starts the accepted run; its id, for the run screen.
    public func confirm() async -> String? {
        guard case .confirm(let prepared) = phase else { return nil }
        phase = .working(.starting)
        do {
            let run = try await dm.reconciler.start(prepared)
            files = []
            rules = ""
            phase = .idle
            return run.id
        } catch {
            phase = .idle
            self.error = message(error)
            return nil
        }
    }

    private func progress(_ step: Step) {
        if case .working = phase { phase = .working(step) }
    }

    private func message(_ error: any Error) -> String { (error as? ApiError)?.message ?? dm.texts.t("errors.unknown") }
}
