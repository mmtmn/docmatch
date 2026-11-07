// Who is signed in on this device. Every sign-in lands on the same account as on the site and on
// Android: the API resolves the person (provider subject, then verified e-mail).
import Foundation
import Observation

/// Not signed in: the sign-ins this deployment offers, and the e-mail step if one is open.
public struct SignedOut: Sendable, Equatable {
    public var methods: AuthMethods
    public var email: String
    public var challengeId: String?
    public var busy: Bool
    public var error: String?

    public init(
        methods: AuthMethods, email: String = "", challengeId: String? = nil, busy: Bool = false, error: String? = nil
    ) {
        self.methods = methods
        self.email = email
        self.challengeId = challengeId
        self.busy = busy
        self.error = error
    }
}

public enum SessionState: Sendable, Equatable {
    case loading
    case signedOut(SignedOut)
    case signedIn(Me)
}

@MainActor
@Observable
public final class SessionModel {
    public private(set) var state: SessionState = .loading
    public private(set) var accounts: [AccountMembership] = []
    public private(set) var accountID: String?
    public private(set) var switching = false
    public private(set) var accountError: String?
    public var hasUnsavedProjectChanges = false
    public var hasActiveProjectWork = false
    @ObservationIgnored private var identityID: String?
    @ObservationIgnored private var generation = 0
    public var currentAccount: AccountMembership? { accounts.first { $0.id == accountID } }
    public var currentDM: Docmatch { accountID.map { dm.inAccount($0) } ?? dm }
    @ObservationIgnored public let dm: Docmatch
    @ObservationIgnored private let cleanup: @Sendable () -> Void
    @ObservationIgnored private let accountStore: any SecretStore

    private struct RememberedAccount: Codable {
        let origin: String
        let userID: String
        let accountID: String
    }

    private func rememberedAccount(for userID: String) -> String? {
        guard let data = accountStore.read(),
            let saved = try? JSONDecoder().decode(RememberedAccount.self, from: data),
            saved.origin == dm.baseURL.absoluteString, saved.userID == userID
        else { return nil }
        return saved.accountID
    }

    private func rememberAccount() {
        guard let identityID, let accountID else { return }
        accountStore.write(try? JSONEncoder().encode(RememberedAccount(
            origin: dm.baseURL.absoluteString, userID: identityID, accountID: accountID)))
    }

    /// `cleanup` removes what a session leaves on the device (exported reports) when it ends.
    public init(
        dm: Docmatch, accountStore: any SecretStore = MemorySecretStore(),
        cleanup: @escaping @Sendable () -> Void = { RunModel.clearExports() }
    ) {
        self.dm = dm
        self.accountStore = accountStore
        self.cleanup = cleanup
        // Revoked from another device's account page (or ended on the server): back to sign-in
        // with the API's own message, wherever the user was.
        dm.transport.onSessionEnded { [weak self] e in
            Task { @MainActor in await self?.ended(e) }
        }
    }

    public func refresh() async {
        let generation = self.generation
        state = .loading
        let next = await load()
        if Task.isCancelled || generation != self.generation { return }  // the screen went away; its replacement loads again
        state = next
    }

    /// After a sign-out (here or from another device): back to the sign-in, with a notice.
    public func signedOut(notice: String?) async {
        generation += 1
        let generation = self.generation
        clearAccounts()
        state = .loading
        let methods = await dm.api.methods()
        guard generation == self.generation else { return }
        state = .signedOut(SignedOut(methods: methods, error: notice))
    }

    public func sendCode(_ email: String) async {
        let address = email.trimmingCharacters(in: .whitespacesAndNewlines)
        await signingIn(email: address) { .challenge(try await self.dm.api.emailStart(address).challengeId) }
    }

    public func verifyCode(_ code: String) async {
        guard case .signedOut(let current) = state, let challenge = current.challengeId else { return }
        await signingIn(email: current.email) {
            try await self.dm.api.emailVerify(challengeId: challenge, code: code)
            return .signedIn
        }
    }

    public func changeEmail() {
        guard case .signedOut(var current) = state else { return }
        current.challengeId = nil
        current.error = nil
        state = .signedOut(current)
    }

    public func passwordLogin(email: String, password: String) async {
        await signingIn(email: email) {
            try await self.dm.api.passwordLogin(
                email: email.trimmingCharacters(in: .whitespacesAndNewlines), password: password)
            return .signedIn
        }
    }

    public func devLogin() async {
        await signingIn {
            try await self.dm.api.devLogin()
            return .signedIn
        }
    }

    public func apple(identityToken: String, authorizationCode: String, fullName: String?) async {
        await signingIn {
            try await self.dm.api.appleNative(
                identityToken: identityToken, authorizationCode: authorizationCode, fullName: fullName)
            return .signedIn
        }
    }

    public func google(idToken: String) async {
        await signingIn {
            try await self.dm.api.googleNative(idToken: idToken)
            return .signedIn
        }
    }

    /// A sign-in that ended before reaching the API (nil: the user closed the sheet).
    public func failed(_ message: String?) {
        guard case .signedOut(var current) = state else { return }
        current.busy = false
        current.error = message
        state = .signedOut(current)
    }

    private func clearAccounts() {
        accountStore.write(nil)
        accounts = []
        accountID = nil
        identityID = nil
        switching = false
        accountError = nil
        hasUnsavedProjectChanges = false
        hasActiveProjectWork = false
        cleanup()
    }

    func ended(_ e: ApiError) async {
        guard case .signedIn = state else { return }
        await signedOut(notice: e.message)
    }

    private enum Outcome: Sendable {
        case signedIn
        case challenge(String)
    }

    /// Runs a sign-in step. The e-mail step stays on the sign-in screen with the challenge open;
    /// any other step signed in, and the account is loaded.
    private func signingIn(email: String? = nil, _ step: @MainActor () async throws -> Outcome) async {
        guard case .signedOut(var current) = state, !current.busy else { return }
        if let email { current.email = email }
        current.busy = true
        current.error = nil
        state = .signedOut(current)
        let generation = self.generation
        do {
            let outcome = try await step()
            guard generation == self.generation else { return }
            switch outcome {
            case .challenge(let id):
                current.challengeId = id
                current.busy = false
                state = .signedOut(current)
            case .signedIn:
                let next = await load()
                guard generation == self.generation else { return }
                state = next
            }
        } catch {
            guard generation == self.generation else { return }
            current.busy = false
            current.error = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
            state = .signedOut(current)
        }
    }

    /// A new immutable client keeps already-started requests in their original account.
    public func selectAccount(_ id: String) async {
        guard case .signedIn = state, !switching, id != accountID else { return }
        let generation = self.generation
        switching = true
        accountError = nil
        defer { if generation == self.generation { switching = false } }
        do {
            let available = try await dm.api.organizations()
            guard generation == self.generation else { return }
            guard available.userId == identityID, available.items.contains(where: { $0.id == id }) else {
                let next = await load()
                guard generation == self.generation else { return }
                state = next
                return
            }
            let me = try await dm.inAccount(id).api.me()
            guard generation == self.generation else { return }
            guard let me else {
                await signedOut(notice: nil)
                return
            }
            cleanup()
            accounts = available.items
            accountID = id
            rememberAccount()
            hasUnsavedProjectChanges = false
            state = .signedIn(me)
        } catch {
            guard generation == self.generation else { return }
            accountError = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }

    /// Refresh membership on foreground without replacing an editor that still has access.
    public func validateAccounts() async {
        guard case .signedIn = state, !switching else { return }
        let generation = self.generation
        do {
            let available = try await dm.api.organizations()
            guard generation == self.generation else { return }
            if available.userId != identityID || !available.items.contains(where: { $0.id == accountID }) {
                cleanup()
                hasUnsavedProjectChanges = false
                let next = await load()
                guard generation == self.generation else { return }
                state = next
            } else {
                accounts = available.items
            }
        } catch {
            guard generation == self.generation else { return }
            accountError = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
        }
    }

    private func load() async -> SessionState {
        let generation = self.generation
        do {
            do {
                let available = try await dm.api.organizations()
                guard generation == self.generation else { return state }
                if identityID != available.userId {
                    accountID = rememberedAccount(for: available.userId)
                    cleanup()
                }
                identityID = available.userId
                accounts = available.items
                if !accounts.contains(where: { $0.id == accountID }) {
                    accountID = accounts.first { $0.kind == "personal" }?.id ?? accounts.first?.id
                }
            } catch let e as ApiError where e.status == 404 {
                guard generation == self.generation else { return state }
                // Older deployments can still serve their original personal-account flow.
                accounts = []
                accountID = nil
            }
            if let me = try await currentDM.api.me() {
                guard generation == self.generation else { return state }
                rememberAccount()
                return .signedIn(me)
            }
            guard generation == self.generation else { return state }
            clearAccounts()
            return .signedOut(SignedOut(methods: await dm.api.methods()))
        } catch {
            let message = (error as? ApiError)?.message ?? dm.texts.t("errors.unknown")
            return .signedOut(SignedOut(methods: await dm.api.methods(), error: message))
        }
    }
}
