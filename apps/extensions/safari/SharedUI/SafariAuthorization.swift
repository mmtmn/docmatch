import AuthenticationServices
import SwiftUI

#if os(macOS)
    import AppKit
#else
    import UIKit
#endif

@MainActor
final class SafariAuthorization: NSObject, ObservableObject, ASWebAuthenticationPresentationContextProviding {
    @Published var status = NativeTexts.text("native_intro")
    @Published var presented = false
    private var session: ASWebAuthenticationSession?

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        #if os(macOS)
            NSApp.windows.first(where: { $0.isVisible }) ?? NSWindow()
        #else
            UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
                .flatMap(\.windows).first(where: \.isKeyWindow) ?? UIWindow()
        #endif
    }

    func open(_ url: URL) {
        presented = true
        do {
            let config = try NativeConfiguration.load()
            let store = try config.store()
            if url.host == "auth" {
                guard let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
                    parts.queryItems?.filter({ $0.name == "state" }).count == 1,
                    let state = parts.queryItems?.first(where: { $0.name == "state" })?.value
                else { throw HandoffError.invalid }
                let pending = try store.forState(state)
                try store.complete(pending.id, callback: url)
                status = NativeTexts.text("native_return")
                return
            }
            let id = try config.requestID(url)
            let pending = try store.pending(id)
            guard pending.code == nil, pending.error == nil else { throw HandoffError.expired }
            let target = try config.authorizationURL(for: pending)
            status = NativeTexts.text("native_confirm")
            #if os(macOS)
                // Keep consent in Safari even when the default browser belongs to a different account/profile.
                guard let safari = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.Safari") else {
                    throw HandoffError.unavailable
                }
                NSWorkspace.shared.open(
                    [target], withApplicationAt: safari, configuration: NSWorkspace.OpenConfiguration()
                ) { _, error in
                    Task { @MainActor in
                        if error != nil { self.status = NativeTexts.text("native_failed") }
                    }
                }
            #else
                guard session == nil else { return }
                let auth = ASWebAuthenticationSession(url: target, callbackURLScheme: config.redirectURI.scheme) {
                    callback, error in
                    Task { @MainActor in
                        self.session = nil
                        do {
                            if let callback {
                                try store.complete(id, callback: callback)
                            } else {
                                try store.cancel(id, profile: pending.profile)
                            }
                            self.status = NativeTexts.text(error == nil ? "native_return" : "native_cancelled")
                        } catch { self.status = NativeTexts.text("native_expired") }
                    }
                }
                auth.presentationContextProvider = self
                session = auth
                if !auth.start() {
                    session = nil
                    status = NativeTexts.text("native_failed")
                }
            #endif
        } catch { status = NativeTexts.text("native_expired") }
    }
}
