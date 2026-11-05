import AuthenticationServices
import DocmatchKit
import SwiftUI

/// Only the sign-ins the API offers; on a development API the local sign-in comes first. Every one
/// of them lands on the same account as the site's and Android's (the API resolves the person).
struct LoginView: View {
    let state: SignedOut
    let session: SessionModel
    @State private var email = ""
    @State private var code = ""
    @State private var localError: String?
    @Environment(\.texts) private var texts
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession

    init(state: SignedOut, session: SessionModel) {
        self.state = state
        self.session = session
    }

    var body: some View {
        let m = state.methods
        let google = m.google ? AppConfig.shared.google : nil
        let usable = m.email || m.dev || m.apple || google != nil
        Form {
            Section {
                Text(texts.t("auth.login_title"))
                    .font(.title2.bold())
                    .accessibilityAddTraits(.isHeader)
                Text(texts.t("auth.login_text"))
                    .foregroundStyle(.secondary)
            }

            if m.dev || m.apple || google != nil {
                Section {
                    if m.dev {
                        Button(state.busy ? texts.t("auth.dev_signing_in") : texts.t("auth.dev_sign_in")) {
                            Task { await session.devLogin() }
                        }
                        .disabled(state.busy)
                    }
                    if m.apple {
                        SignInWithAppleButton(.signIn) { request in
                            request.requestedScopes = [.fullName, .email]
                        } onCompletion: { result in
                            apple(result)
                        }
                        .frame(height: 44)
                        .disabled(state.busy)
                    }
                    if let google {
                        Button(texts.t("auth.sign_in_google")) {
                            Task { await signInWithGoogle(google) }
                        }
                        .disabled(state.busy)
                    }
                }
            }

            if m.email {
                Section {
                    if state.challengeId == nil {
                        TextField(texts.t("auth.email_label"), text: $email)
                            .keyboardType(.emailAddress)
                            .textContentType(.emailAddress)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        Button(texts.t("auth.send_code")) {
                            let address = email.trimmingCharacters(in: .whitespacesAndNewlines)
                            guard address.contains("@"), address.split(separator: "@").last?.contains(".") == true else {
                                localError = texts.t("errors.invalid_email")
                                return
                            }
                            localError = nil
                            Task { await session.sendCode(address) }
                        }
                        .disabled(state.busy)
                    } else {
                        Text(texts.t("mobile.code_sent", ["email": state.email]))
                        TextField(texts.t("auth.code_label"), text: $code)
                            .keyboardType(.numberPad)
                            .textContentType(.oneTimeCode)
                        Button(texts.t("auth.verify")) {
                            Task { await session.verifyCode(code) }
                        }
                        .disabled(state.busy || code.count != 6)
                        Button(texts.t("auth.change_email")) { session.changeEmail() }
                    }
                }
            }

            if !usable {
                // Nothing this app can use; usually the API is out of reach.
                Section {
                    Text(texts.t("auth.none_available"))
                    Button(texts.t("mobile.retry")) { Task { await session.refresh() } }
                }
            }

            if let error = localError ?? state.error {
                Text(error)
                    .foregroundStyle(Color(DmColors.statusDanger))
            }

            #if DEBUG
            Text(texts.t("mobile.server", ["url": AppConfig.shared.apiBaseURL.absoluteString]))
                .font(.footnote)
                .foregroundStyle(.secondary)
            #endif
        }
        .onChange(of: code) { _, value in
            let digits = String(value.filter(\.isNumber).prefix(6))
            if digits != value { code = digits }
        }
        .onChange(of: email) { _, value in
            if value.count > Limits.Text.email { email = String(value.prefix(Limits.Text.email)) }
        }
    }

    /// Sign in with Apple: the identity token and the authorization code go to the API, which
    /// resolves the account by Apple's subject (the same for the site's Services ID).
    private func apple(_ result: Result<ASAuthorization, any Error>) {
        switch result {
        case .success(let authorization):
            guard let credential = authorization.credential as? ASAuthorizationAppleIDCredential,
                let tokenData = credential.identityToken, let token = String(data: tokenData, encoding: .utf8),
                let codeData = credential.authorizationCode, let authorizationCode = String(data: codeData, encoding: .utf8)
            else {
                session.failed(texts.t("auth.error_generic"))
                return
            }
            let name = [credential.fullName?.givenName, credential.fullName?.familyName].compactMap { $0 }.joined(separator: " ")
            Task { await session.apple(identityToken: token, authorizationCode: authorizationCode, fullName: name.isEmpty ? nil : name) }
        case .failure(let error):
            if (error as? ASAuthorizationError)?.code == .canceled {
                session.failed(nil)
            } else {
                session.failed(texts.t("auth.error_generic"))
            }
        }
    }

    /// Google's installed-app flow (PKCE) in the system's web sheet; its ID token, issued to the
    /// iOS client id, goes to the API like Android's.
    private func signInWithGoogle(_ google: GoogleOAuth) async {
        let attempt = google.begin()
        do {
            let callback = try await webAuthenticationSession.authenticate(
                using: attempt.url, callbackURLScheme: google.callbackScheme, preferredBrowserSession: .shared)
            let code = try google.code(from: callback, for: attempt)
            let (data, response) = try await URLSession.shared.data(for: google.tokenRequest(code: code, for: attempt))
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { throw GoogleOAuth.Failure.noToken }
            await session.google(idToken: try GoogleOAuth.idToken(from: data))
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
            session.failed(nil)
        } catch GoogleOAuth.Failure.cancelled {
            session.failed(nil)
        } catch {
            session.failed(texts.t("auth.error_generic"))
        }
    }
}
