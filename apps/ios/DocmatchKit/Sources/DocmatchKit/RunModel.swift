// One run: followed to the end, its findings by amount at risk, decisions recorded on the API (the
// site and the other devices see them) and the report exported.
import Foundation
import Observation

public enum RunState: Sendable, Equatable {
    case loading
    case running(Step?)
    case done(Run, [Finding])
    case failed(String)
}

@MainActor
@Observable
public final class RunModel {
    public private(set) var state: RunState = .loading
    public var notice: String?
    @ObservationIgnored public let runId: String
    @ObservationIgnored private let dm: Docmatch

    public init(dm: Docmatch, runId: String) {
        self.dm = dm
        self.runId = runId
    }

    public func load() async {
        state = .loading
        do {
            let run = try await dm.reconciler.follow(runId) { step in
                Task { @MainActor [weak self] in self?.state = .running(step) }
            }
            if run.status == RunStatus.completed {
                let findings = try await dm.api.findings(run.id).sorted { $0.amountAtRisk > $1.amountAtRisk }
                state = .done(run, findings)
            } else {
                state = .failed(dm.texts.t("results.live_failed", ["message": run.error?.message ?? dm.texts.t("errors.unknown")]))
            }
        } catch is CancellationError {
            return
        } catch {
            state = .failed((error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
        }
    }

    /// Records the decision on the API and here.
    public func decide(_ finding: Finding, _ decision: String) async {
        guard case .done(let run, let findings) = state else { return }
        do {
            try await dm.api.feedback(runId: runId, findingId: finding.id, decision: decision)
            guard case .done(_, let latest) = state else { return }
            state = .done(run, (latest.isEmpty ? findings : latest).map { f in
                var g = f
                if g.id == finding.id { g.status = decision }
                return g
            })
            notice = dm.texts.t("mobile.finding_decided")
        } catch {
            notice = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }

    /// The report as a file in the temporary folder, for the share sheet; nil when it failed.
    public func export(_ format: String) async -> URL? {
        do {
            let bytes = try await dm.api.export(runId: runId, format: format)
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent("exports", isDirectory: true)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let file = dir.appendingPathComponent("docmatch-\(runId).\(format)")
            #if os(iOS)
            try bytes.write(to: file, options: [.atomic, .completeFileProtection])
            #else
            try bytes.write(to: file, options: .atomic)
            #endif
            return file
        } catch {
            notice = dm.texts.t("mobile.export_failed", ["message": (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")])
            return nil
        }
    }
}
