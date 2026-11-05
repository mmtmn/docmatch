// The history (every run of the account, whichever device started it) and the account page (the
// same as the site's /conta: profile, credits and every signed-in device).
import Foundation
import Observation

public enum HistoryState: Sendable, Equatable {
    case loading
    case ready([Run], next: String?)
    case failed(String)
}

@MainActor
@Observable
public final class HistoryModel {
    public private(set) var state: HistoryState = .loading
    @ObservationIgnored private let dm: Docmatch

    public init(dm: Docmatch) { self.dm = dm }

    public func refresh() async {
        state = .loading
        do {
            let page = try await dm.api.runs()
            state = .ready(page.items, next: page.nextCursor)
        } catch {
            state = .failed((error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
        }
    }

    public func more() async {
        guard case .ready(let runs, let next?) = state else { return }
        // Keep what is shown when a page fails; the next tap tries again.
        guard let page = try? await dm.api.runs(cursor: next) else { return }
        state = .ready(runs + page.items.filter { r in !runs.contains { $0.id == r.id } }, next: page.nextCursor)
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

    public func refresh(notice: String? = nil) async {
        do {
            guard let me = try await dm.api.me() else {
                await onSignedOut(dm.texts.t("account.signed_out"))
                return
            }
            state = .ready(me, try await dm.api.devices(), notice: notice)
        } catch {
            state = .failed((error as? ApiError)?.message ?? dm.texts.t("errors.unknown"))
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

    public static func confidence(_ texts: Texts, _ finding: Finding) -> String {
        switch finding.confidenceLabel {
        case "confirmed": return texts.t("results.confidence_high")
        case "probable": return texts.t("results.confidence_medium")
        default: return texts.t("results.confidence_low")
        }
    }
}
