import Foundation
import XCTest
@testable import DocmatchKit

final class GoogleOAuthTests: XCTestCase {
    func testOnlyARealIOSClientIdShowsTheButton() {
        XCTAssertNil(GoogleOAuth(clientId: ""))
        XCTAssertNil(GoogleOAuth(clientId: "qualquer-coisa"))
        XCTAssertNil(GoogleOAuth(clientId: "a/b.apps.googleusercontent.com"))
        let google = GoogleOAuth(clientId: " 123-abc.apps.googleusercontent.com ")
        XCTAssertEqual(google?.callbackScheme, "com.googleusercontent.apps.123-abc")
        XCTAssertEqual(google?.redirectURI, "com.googleusercontent.apps.123-abc:/oauth2redirect")
    }

    func testTheAuthorizationUsesPKCEAndAFreshState() throws {
        let google = try XCTUnwrap(GoogleOAuth(clientId: "123-abc.apps.googleusercontent.com"))
        let a = google.begin()
        let b = google.begin()
        XCTAssertNotEqual(a.verifier, b.verifier)
        XCTAssertNotEqual(a.state, b.state)
        XCTAssertGreaterThanOrEqual(a.verifier.count, 43, "RFC 7636: 43 a 128 caracteres")
        let items = Dictionary(uniqueKeysWithValues: (URLComponents(url: a.url, resolvingAgainstBaseURL: false)?.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        XCTAssertEqual(a.url.host, "accounts.google.com")
        XCTAssertEqual(items["code_challenge_method"], "S256")
        XCTAssertEqual(items["code_challenge"], GoogleOAuth.base64URL(Reconciler.sha256(Data(a.verifier.utf8))))
        XCTAssertEqual(items["state"], a.state)
        XCTAssertEqual(items["scope"], "openid email profile")
        XCTAssertEqual(items["redirect_uri"], google.redirectURI)
    }

    func testTheCallbackMustAnswerThisAttempt() throws {
        let google = try XCTUnwrap(GoogleOAuth(clientId: "123-abc.apps.googleusercontent.com"))
        let attempt = google.begin()
        let ok = URL(string: "\(google.redirectURI)?state=\(attempt.state)&code=4%2Fabc")!
        XCTAssertEqual(try google.code(from: ok, for: attempt), "4/abc")
        XCTAssertThrowsError(try google.code(from: URL(string: "\(google.redirectURI)?state=outro&code=x")!, for: attempt)) {
            XCTAssertEqual($0 as? GoogleOAuth.Failure, .stateMismatch)
        }
        XCTAssertThrowsError(try google.code(from: URL(string: "\(google.redirectURI)?error=access_denied")!, for: attempt)) {
            XCTAssertEqual($0 as? GoogleOAuth.Failure, .cancelled)
        }
        let token = google.tokenRequest(code: "4/abc", for: attempt)
        let body = String(decoding: token.httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(body.contains("code=4%2Fabc") && body.contains("code_verifier=\(attempt.verifier)") && body.contains("grant_type=authorization_code"), body)
        XCTAssertFalse(body.contains("client_secret"), "cliente público: PKCE, sem segredo")
        XCTAssertEqual(try GoogleOAuth.idToken(from: Data(#"{"id_token":"a.b.c","access_token":"x"}"#.utf8)), "a.b.c")
        XCTAssertThrowsError(try GoogleOAuth.idToken(from: Data("{}".utf8)))
    }

    func testPKCEMatchesRFC7636AppendixB() {
        // RFC 7636, Appendix B: the verifier and its S256 challenge.
        let verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        XCTAssertEqual(GoogleOAuth.base64URL(Reconciler.sha256(Data(verifier.utf8))), "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
    }
}
