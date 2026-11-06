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
        XCTAssertThrowsError(try google.code(from: URL(string: "\(google.redirectURI)?state=\(attempt.state)&error=access_denied")!, for: attempt)) {
            XCTAssertEqual($0 as? GoogleOAuth.Failure, .cancelled)
        }
        let token = google.tokenRequest(code: "4/abc", for: attempt)
        let body = String(decoding: token.httpBody ?? Data(), as: UTF8.self)
        XCTAssertTrue(body.contains("code=4%2Fabc") && body.contains("code_verifier=\(attempt.verifier)") && body.contains("grant_type=authorization_code"), body)
        XCTAssertFalse(body.contains("client_secret"), "cliente público: PKCE, sem segredo")
        XCTAssertEqual(try GoogleOAuth.idToken(from: Data(#"{"id_token":"a.b.c","access_token":"x"}"#.utf8)), "a.b.c")
        XCTAssertThrowsError(try GoogleOAuth.idToken(from: Data("{}".utf8)))
    }

    func testCallbackRejectsWrongDestinationAndAmbiguousResults() throws {
        let google = try XCTUnwrap(GoogleOAuth(clientId: "123-abc.apps.googleusercontent.com"))
        let attempt = google.begin()
        let query = "state=\(attempt.state)&code=ok"
        let invalid = [
            "other:/oauth2redirect?\(query)",
            "\(google.callbackScheme):/wrong?\(query)",
            "\(google.callbackScheme)://host/oauth2redirect?\(query)",
            "\(google.redirectURI)?\(query)#code=other",
            "\(google.redirectURI)?\(query)&code=other",
            "\(google.redirectURI)?\(query)&error=access_denied",
            "\(google.redirectURI)?state=\(attempt.state)&error=access_denied&error=other",
            "\(google.redirectURI)?state=\(attempt.state)&error=",
        ]
        for raw in invalid {
            XCTAssertThrowsError(try google.code(from: XCTUnwrap(URL(string: raw)), for: attempt)) {
                XCTAssertEqual($0 as? GoogleOAuth.Failure, .invalidCallback)
            }
        }
    }

    func testStateIsRequiredAndUniqueEvenForDenials() throws {
        let google = try XCTUnwrap(GoogleOAuth(clientId: "123-abc.apps.googleusercontent.com"))
        let attempt = google.begin()
        for query in [
            "error=access_denied", "state=wrong&error=access_denied",
            "state=\(attempt.state)&state=\(attempt.state)&code=ok",
            "state=\(attempt.state)&state=wrong&error=access_denied",
            "code=ok",
        ] {
            XCTAssertThrowsError(try google.code(from: XCTUnwrap(URL(string: "\(google.redirectURI)?\(query)")), for: attempt)) {
                XCTAssertEqual($0 as? GoogleOAuth.Failure, .stateMismatch)
            }
        }
        XCTAssertThrowsError(try google.code(from: XCTUnwrap(URL(string: "\(google.redirectURI)?state=\(attempt.state)&error=server_error")), for: attempt)) {
            XCTAssertEqual($0 as? GoogleOAuth.Failure, .refused("server_error"))
        }
        XCTAssertThrowsError(try google.code(from: XCTUnwrap(URL(string: "\(google.redirectURI)?state=\(attempt.state)&code=")), for: attempt)) {
            XCTAssertEqual($0 as? GoogleOAuth.Failure, .refused("no_code"))
        }
    }

    func testConfiguredDebugClientAndTokenExchange() throws {
        let client = "118512591918-ui6bljd5o1jlghgsc5q67igbs755l9ou.apps.googleusercontent.com"
        let google = try XCTUnwrap(GoogleOAuth(clientId: client))
        XCTAssertEqual(google.redirectURI, "com.googleusercontent.apps.118512591918-ui6bljd5o1jlghgsc5q67igbs755l9ou:/oauth2redirect")
        let attempt = google.begin()
        let request = google.tokenRequest(code: "a+b&c=d /", for: attempt)
        XCTAssertEqual(request.url, GoogleOAuth.tokenEndpoint)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/x-www-form-urlencoded")
        let body = String(decoding: try XCTUnwrap(request.httpBody), as: UTF8.self)
        XCTAssertTrue(body.contains("code=a%2Bb%26c%3Dd%20%2F"))
        let fields = try XCTUnwrap(URLComponents(string: "https://example.test/?\(body)")?.queryItems)
        XCTAssertEqual(fields.first { $0.name == "client_id" }?.value, client)
        XCTAssertEqual(fields.first { $0.name == "redirect_uri" }?.value, google.redirectURI)
        XCTAssertEqual(fields.first { $0.name == "code_verifier" }?.value, attempt.verifier)
        for raw in ["{}", "not json", #"{"error":"invalid_grant"}"#, #"{"id_token":""}"#, #"{"id_token":42}"#] {
            XCTAssertThrowsError(try GoogleOAuth.idToken(from: Data(raw.utf8))) {
                XCTAssertEqual($0 as? GoogleOAuth.Failure, .noToken)
            }
        }
    }

    func testPKCEMatchesRFC7636AppendixB() {
        // RFC 7636, Appendix B: the verifier and its S256 challenge.
        let verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        XCTAssertEqual(GoogleOAuth.base64URL(Reconciler.sha256(Data(verifier.utf8))), "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
    }
}
