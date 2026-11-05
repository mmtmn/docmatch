// The history (every run of the account, whichever device started it) and the account page (the
// same as the site's /conta: profile, credits and every signed-in device).
import Foundation
import Observation

public enum HistoryState: Sendable, Equatable {
    case loading
    case ready([Run], next: String?, notice: String?)
    case failed(String)
}

@MainActor
@Observable
public final class HistoryModel {
    public private(set) var state: HistoryState = .loading
    @ObservationIgnored private let dm: Docmatch

    public init(dm: Docmatch) { self.dm = dm }

    /// Reloads the first page (every time the tab is shown); what is on screen stays meanwhile.
    public func refresh() async {
        if case .ready = state {} else { state = .loading }
        do {
            let page = try await dm.api.runs()
            state = .ready(page.items, next: page.nextCursor, notice: nil)
        } catch {
            if error is CancellationError || Task.isCancelled { return }
            let message = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
            if case .ready(let runs, let next, _) = state {
                state = .ready(runs, next: next, notice: message)
            } else {
                state = .failed(message)
            }
        }
    }

    public func more() async {
        guard case .ready(let runs, let next?, _) = state else { return }
        do {
            let page = try await dm.api.runs(cursor: next)
            // Offset pages shift when a run starts meanwhile: never show one twice.
            state = .ready(runs + page.items.filter { r in !runs.contains { $0.id == r.id } }, next: page.nextCursor, notice: nil)
        } catch {
            state = .ready(runs, next: next, notice: dm.texts.t("mobile.load_more_failed"))
        }
    }
}

public enum AccountState: Sendable, Equatable {
    case loading
    case ready(Me, [Device], notice: String?)
    case failed(String)
}

@MainActor
@Observable
public final class AccountModel {
    public private(set) var state: AccountState = .loading
    @ObservationIgnored private let dm: Docmatch
    @ObservationIgnored private let onSignedOut: @MainActor (String?) async -> Void

    public init(dm: Docmatch, onSignedOut: @escaping @MainActor (String?) async -> Void) {
        self.dm = dm
        self.onSignedOut = onSignedOut
    }

    /// Reloads the account (every time the tab is shown); what is on screen stays meanwhile.
    public func refresh(notice: String? = nil) async {
        do {
            guard let me = try await dm.api.me() else {
                await onSignedOut(dm.texts.t("account.signed_out"))
                return
            }
            state = .ready(me, try await dm.api.devices(), notice: notice)
        } catch {
            if error is CancellationError || Task.isCancelled { return }
            let message = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
            if case .ready(let me, let devices, _) = state {
                state = .ready(me, devices, notice: message)
            } else {
                state = .failed(message)
            }
        }
    }

    public func revoke(_ device: Device) async {
        do {
            try await dm.api.revokeDevice(device)
            if device.current {
                await onSignedOut(dm.texts.t("account.signed_out"))
            } else {
                await refresh(notice: dm.texts.t("account.revoked"))
            }
        } catch {
            if case .ready(let me, let devices, _) = state {
                state = .ready(me, devices, notice: (error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
            }
        }
    }

    public func signOut() async {
        try? await dm.api.logout()
        await onSignedOut(dm.texts.t("account.signed_out"))
    }

    /// Deletes the account (after the screen's confirmation) and returns to the sign-in.
    public func deleteAccount() async {
        do {
            let when = try await dm.api.deleteAccount()
            await onSignedOut(dm.texts.t("mobile.delete_done", ["date": Format.date(when)]))
        } catch {
            if case .ready(let me, let devices, _) = state {
                state = .ready(me, devices, notice: (error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
            }
        }
    }
}

/// The labels every screen shares, from the same keys Android and the site use.
public enum Labels {
    public static func step(_ texts: Texts, _ step: Step) -> String {
        switch step {
        case .uploading(let done, let total): return texts.t("app.uploading", ["done": done, "total": total])
        case .scanning(let name): return texts.t("app.scanning", ["name": name])
        case .estimating: return texts.t("app.estimating")
        case .starting: return texts.t("app.starting")
        case .running(let message):
            if let message, !message.isEmpty { return texts.t("results.live_running", ["message": message]) }
            return texts.t("mobile.status_running")
        }
    }

    public static func runStatus(_ texts: Texts, _ status: String) -> String {
        switch status {
        case RunStatus.queued: return texts.t("mobile.status_queued")
        case RunStatus.running: return texts.t("mobile.status_running")
        case RunStatus.needsApproval: return texts.t("mobile.status_needs_approval")
        case RunStatus.completed: return texts.t("mobile.status_completed")
        case RunStatus.failed: return texts.t("mobile.status_failed")
        default: return texts.t("mobile.status_cancelled")
        }
    }

    public static func findingStatus(_ texts: Texts, _ status: String) -> String {
        switch status {
        case Decision.confirmed: return texts.t("results.status_accepted")
        case Decision.dismissed: return texts.t("results.status_rejected")
        case Decision.resolved: return texts.t("results.status_edited")
        default: return texts.t("results.status_open")
        }
    }

    /// finding.json `confidence_label` (Confirmado, Provável, Precisa de você) as the site's level.
    public static func confidence(_ texts: Texts, _ finding: Finding) -> String {
        switch finding.confidenceLabel {
        case "Confirmado": return texts.t("results.confidence_high")
        case "Provável": return texts.t("results.confidence_medium")
        default: return texts.t("results.confidence_low")
        }
    }

    /// The site's label for a kind (dropzone.kind_*).
    public static func kind(_ texts: Texts, _ kind: FileKind) -> String { texts.t("dropzone.kind_\(kind.rawValue)") }

    /// "2 arquivos · 1,5 MB no total".
    public static func files(_ texts: Texts, count: Int, bytes: Int64) -> String {
        let n = count == 1 ? texts.t("dropzone.files_count_one") : texts.t("dropzone.files_count_many", ["count": count])
        return n + " · " + texts.t("dropzone.total_size", ["size": Format.size(texts, bytes)])
    }

    /// The site's counter text, from 80% of the limit (nil below it).
    public static func counter(_ texts: Texts, count: Int, max: Int) -> String? {
        if count < Int((Double(max) * 0.8).rounded(.up)) { return nil }
        return count >= max ? texts.t("form.counter_limit", ["max": max]) : texts.t("form.counter", ["count": count, "max": max])
    }
}
