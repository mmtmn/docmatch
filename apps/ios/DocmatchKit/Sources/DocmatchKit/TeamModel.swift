import Foundation
import Observation

public enum TeamAction: Sendable {
    case convert(String)
    case invite(String, String)
    case resend(String)
    case revoke(String)
    case role(String, String)
    case remove(String)
    case transfer(String)
    case leave
}

@MainActor @Observable
public final class TeamModel {
    public private(set) var team: TeamInfo?
    public private(set) var busy = false
    public private(set) var error: String?
    public private(set) var notice: String?
    public private(set) var left = false
    @ObservationIgnored private let dm: Docmatch
    public init(dm: Docmatch) { self.dm = dm }
    public func load() async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do { team = try await dm.api.team() } catch { fail(error) }
    }
    public func perform(_ action: TeamAction) async {
        guard !busy else { return }
        busy = true
        error = nil
        notice = nil
        defer { busy = false }
        do {
            switch action {
            case .convert(let name): team = try await dm.api.convertToTeam(name: name)
            case .invite(let email, let role):
                _ = try await dm.api.invite(email: email, role: role)
                notice = dm.texts.t("teams.invite_sent")
            case .resend(let id):
                try await dm.api.resendInvitation(id)
                notice = dm.texts.t("teams.resent")
            case .revoke(let id): try await dm.api.revokeInvitation(id)
            case .role(let id, let role): try await dm.api.changeMemberRole(id, role: role)
            case .remove(let id): try await dm.api.removeMember(id)
            case .transfer(let id): try await dm.api.transferOwnership(id)
            case .leave:
                try await dm.api.leaveTeam()
                team = nil
                left = true
                return
            }
            team = try await dm.api.team()
        } catch { fail(error) }
    }
    private func fail(_ e: any Error) {
        if e is CancellationError { return }
        if let e = e as? ApiError, [401, 403, 404].contains(e.status) { team = nil }
        error = (e as? ApiError)?.message ?? dm.texts.t("errors.unknown")
    }
}

/// Opening a link only loads a preview. Membership needs the separate, explicit response.
@MainActor @Observable
public final class InvitationModel {
    public private(set) var preview: InvitationPreview?
    public private(set) var response: InvitationResponse?
    public private(set) var busy = false
    public private(set) var error: String?
    @ObservationIgnored private let dm: Docmatch
    @ObservationIgnored private let token: String
    public init(dm: Docmatch, token: String) {
        self.dm = dm
        self.token = token
    }
    public func load() async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do { preview = try await dm.api.invitation(token: token) } catch {
            preview = nil
            self.error = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }
    public func respond(accept: Bool) async {
        guard !busy, preview?.emailMatches == true,
            preview?.status == "pending" || (accept && preview?.status == "accepted")
        else { return }
        busy = true
        error = nil
        defer { busy = false }
        do { response = try await dm.api.respondToInvitation(token: token, accept: accept) } catch {
            self.error = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }
}

public enum InvitationLink {
    public static func token(_ url: URL, site: URL) -> String? {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false), c.user == nil, c.password == nil else {
            return nil
        }
        let web = c.scheme == site.scheme && c.host == site.host && c.port == site.port && c.path == "/convite"
        let app = c.scheme == "docmatch" && c.host == "convite" && c.path.isEmpty && c.port == nil
        guard web || app, let fragment = c.fragment,
            let parts = URLComponents(string: "?" + fragment)?.queryItems,
            parts.count == 1, parts[0].name == "token", let token = parts[0].value,
            (32...128).contains(token.utf8.count),
            token.utf8.allSatisfy({ byte in
                (65...90).contains(byte) || (97...122).contains(byte) || (48...57).contains(byte) || byte == 45
                    || byte == 95
            })
        else { return nil }
        return token
    }
}
