import AuthenticationServices
import DocmatchKit
import SwiftUI

struct LoginView: View {
    let state: SignedOut
    let session: SessionModel
    @State private var email = ""
    @State private var code = ""
    @State private var password = ""
    @State private var showPassword = false
    @State private var useCode = false
    @State private var localError: String?
    @State private var providerBusy = false
    @FocusState private var passwordFocused: Bool
    @Environment(\.texts) private var texts
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession
    @Environment(\.colorScheme) private var colorScheme

    init(state: SignedOut, session: SessionModel) { self.state = state; self.session = session }
    private var busy: Bool { state.busy || providerBusy }
    private var google: GoogleOAuth? { state.methods.google ? AppConfig.shared.google : nil }

    var body: some View {
        DmPage {
            VStack(spacing: 14) {
                DmIconTile(name: "doc.text.magnifyingglass")
                Text(texts.t("auth.login_title")).font(.title2.bold()).accessibilityAddTraits(.isHeader)
                Text(texts.t("auth.login_text")).font(.subheadline).foregroundStyle(Color(DmColors.textMuted)).multilineTextAlignment(.center)
            }.frame(maxWidth: .infinity).padding(.vertical, 12)
            DmCard {
                providers
                if state.methods.password && !useCode { passwordForm }
                if state.methods.email && state.methods.password {
                    Button(texts.t(useCode ? "auth.use_password" : "auth.send_code")) {
                        useCode.toggle(); session.changeEmail(); password = ""; localError = nil
                    }.font(.subheadline).frame(maxWidth: .infinity, minHeight: 44)
                }
                if state.methods.email && (!state.methods.password || useCode) { emailCodeForm }
                if !(state.methods.email || state.methods.password || state.methods.dev || state.methods.apple || google != nil) {
                    Text(texts.t("auth.none_available")).font(.subheadline)
                    Button(texts.t("mobile.retry")) { Task { await session.refresh() } }.buttonStyle(DmButtonStyle(.secondary))
                }
                if let error = localError ?? state.error { DmNotice(message: error, error: true) }
            }
            .disabled(busy)
            Text(texts.t("mobile.legal_text")).font(.caption).foregroundStyle(Color(DmColors.textMuted)).multilineTextAlignment(.center).frame(maxWidth: .infinity)
        }
        .onChange(of: code) { _, value in
            let digits = String(value.filter(\.isNumber).prefix(6)); if digits != value { code = digits }
        }
        .onChange(of: email) { _, value in
            if value.count > Limits.Text.email { email = String(value.prefix(Limits.Text.email)) }
        }
    }

    private var providers: some View {
        VStack(spacing: 12) {
            if state.methods.dev {
                Button(busy ? texts.t("auth.dev_signing_in") : texts.t("auth.dev_sign_in")) { Task { await session.devLogin() } }
                    .buttonStyle(DmButtonStyle())
            }
            if let google {
                Button {
                    providerBusy = true
                    Task { defer { providerBusy = false }; await signInWithGoogle(google) }
                } label: {
                    HStack(spacing: 12) {
                        Image("GoogleMark").resizable().frame(width: 20, height: 20).accessibilityHidden(true)
                        Text(texts.t("auth.sign_in_google"))
                    }
                }
                .buttonStyle(DmButtonStyle(.secondary))
            }
            if state.methods.apple {
                SignInWithAppleButton(.signIn) { request in request.requestedScopes = [.fullName, .email] }
                    onCompletion: { apple($0) }
                    .signInWithAppleButtonStyle(colorScheme == .dark ? .white : .black)
                    .frame(height: 48).clipShape(RoundedRectangle(cornerRadius: DmSize.radiusMd))
            }
            if (state.methods.password || state.methods.email) && (state.methods.dev || state.methods.apple || google != nil) {
                HStack { Divider(); Text(texts.t("auth.or")).font(.caption).foregroundStyle(Color(DmColors.textMuted)); Divider() }
                    .padding(.vertical, 4)
            }
        }
    }

    private var emailField: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(texts.t("auth.email_label")).font(.subheadline.weight(.semibold))
            TextField(texts.t("auth.email_label"), text: $email)
                .keyboardType(.emailAddress).textContentType(.username).textInputAutocapitalization(.never).autocorrectionDisabled().dmField()
        }
    }

    private var passwordForm: some View {
        VStack(alignment: .leading, spacing: 14) {
            emailField
            Text(texts.t("auth.password_label")).font(.subheadline.weight(.semibold))
            HStack {
                Group {
                    if showPassword { TextField(texts.t("auth.password_label"), text: $password) }
                    else { SecureField(texts.t("auth.password_label"), text: $password) }
                }.textContentType(.password).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .focused($passwordFocused).submitLabel(.go).onSubmit { passwordLogin() }
                Button { showPassword.toggle(); passwordFocused = true } label: {
                    Image(systemName: showPassword ? "eye.slash" : "eye").frame(width: 44, height: 44)
                }.buttonStyle(.plain).accessibilityLabel(texts.t(showPassword ? "auth.hide_password" : "auth.show_password"))
            }.dmField()
            Button(texts.t(busy ? "auth.signing_in" : "nav.login")) { passwordLogin() }
                .buttonStyle(DmButtonStyle()).disabled(password.isEmpty || !email.contains("@"))
        }
    }

    private func passwordLogin() {
        guard !busy, !password.isEmpty, email.contains("@") else { return }
        let secret = password; password = ""; passwordFocused = false
        Task { await session.passwordLogin(email: email, password: secret) }
    }

    @ViewBuilder private var emailCodeForm: some View {
        if state.challengeId == nil {
            emailField
            Button(texts.t("auth.send_code")) {
                let address = email.trimmingCharacters(in: .whitespacesAndNewlines)
                guard address.contains("@"), address.split(separator: "@").last?.contains(".") == true else {
                    localError = texts.t("errors.invalid_email"); return
                }
                localError = nil; Task { await session.sendCode(address) }
            }.buttonStyle(DmButtonStyle())
        } else {
            Text(texts.t("mobile.code_sent", ["email": state.email])).font(.subheadline)
            TextField(texts.t("auth.code_label"), text: $code).keyboardType(.numberPad).textContentType(.oneTimeCode).dmField()
            Button(texts.t("auth.verify")) { Task { await session.verifyCode(code) } }
                .buttonStyle(DmButtonStyle()).disabled(code.count != 6)
            Button(texts.t("auth.change_email")) { session.changeEmail() }.frame(minHeight: 44)
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
        localError = nil
        session.failed(nil)
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
