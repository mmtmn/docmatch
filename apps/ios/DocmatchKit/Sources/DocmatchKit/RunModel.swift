// One run: followed while its screen is open, parked runs approved, findings by amount at risk,
// decisions recorded on the API (the site and the other devices see them), the report exported.
import Foundation
import Observation

public enum RunState: Sendable, Equatable {
    case loading
    case running(Step?)
    /// Parked until the user approves the extra estimate (pending_quote).
    case needsApproval(Run, busy: Bool)
    case done(Run, [Finding])
    case failed(String)
}

@MainActor
@Observable
public final class RunModel {
    public private(set) var state: RunState = .loading
    public var notice: String?
    /// Bumped to follow the run again (retry, after an approval); the screen keys its task on it.
    public private(set) var attempt = 0
    @ObservationIgnored public let runId: String
    @ObservationIgnored private let dm: Docmatch
    @ObservationIgnored private var approvalKey: (quote: String, key: String)?

    public init(dm: Docmatch, runId: String) {
        self.dm = dm
        self.runId = runId
    }

    /// Follows the run while the screen is shown: the screen's task calls this and cancels it on
    /// leaving, so a run left open does not poll in the background.
    public func follow() async {
        if case .done = state { return }
        do {
            let run = try await dm.reconciler.follow(runId) { step in
                Task { @MainActor [weak self] in self?.state = .running(step) }
            }
            switch run.status {
            case RunStatus.completed:
                let findings = try await dm.api.findings(run.id).sorted { $0.amountAtRisk > $1.amountAtRisk }
                state = .done(run, findings)
            case RunStatus.needsApproval:
                state = .needsApproval(run, busy: false)
            default:
                state = .failed(dm.texts.t("results.live_failed", ["message": run.error?.message ?? dm.texts.t("errors.unknown")]))
            }
        } catch {
            if error is CancellationError || Task.isCancelled { return }
            state = .failed((error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
        }
    }

    public func retry() {
        state = .loading
        attempt += 1
    }

    /// Approves the extra estimate; the same key on a retry approves it once.
    public func approve() async {
        guard case .needsApproval(let run, false) = state, let quote = run.pendingQuote else { return }
        let key = approvalKey?.quote == quote.id ? approvalKey!.key : UUID().uuidString.lowercased()
        approvalKey = (quote.id, key)
        state = .needsApproval(run, busy: true)
        do {
            _ = try await dm.api.approveQuote(runId: runId, quote: quote, idempotencyKey: key)
            state = .running(nil)
            attempt += 1
        } catch {
            state = .needsApproval(run, busy: false)
            notice = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }

    /// Records the decision (with the note of an edit) on the API and here.
    public func decide(_ finding: Finding, _ decision: String, note: String? = nil) async {
        guard case .done = state else { return }
        notice = nil
        do {
            try await dm.api.feedback(runId: runId, findingId: finding.id, decision: decision, note: note)
            guard case .done(let run, let latest) = state else { return }
            state = .done(run, latest.map { f in
                var g = f
                if g.id == finding.id { g.status = decision }
                return g
            })
            notice = dm.texts.t("mobile.finding_decided")
        } catch {
            notice = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }

    /// The report (XLSX or CSV) as a file in the temporary folder, for the share sheet.
    public func export(_ format: String) async -> URL? {
        do {
            let bytes = try await dm.api.export(runId: runId, format: format)
            let dir = Self.exportsDirectory
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

    /// Where exports live, only while the session does (cleared when it ends and at every start).
    public nonisolated static var exportsDirectory: URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("exports", isDirectory: true)
    }

    public nonisolated static func clearExports() {
        try? FileManager.default.removeItem(at: exportsDirectory)
    }
}
