import Foundation
import XCTest

@testable import DocmatchKit

@MainActor final class InvitationTests: XCTestCase {
    private let preview =
        #"{"id":"inv_1","organization":"Empresa","email_hint":"p***@example.test","role":"reconciler","status":"pending","expires_at":"2026-11-12T10:00:00Z","email_matches":true}"#
    func testOpeningAnInvitationDoesNotAcceptAndAnExplicitResponseUsesIdentity() async throws {
        let server = FakeServer()
        let dm = try newClient(server).inAccount("org_personal")
        let model = InvitationModel(dm: dm, token: "link-token")
        server.enqueue(200, preview)
        await model.load()
        let opened = server.take()
        XCTAssertEqual(opened.url?.path, "/v1/invitations/preview")
        XCTAssertNil(header(opened, "X-Docmatch-Org"))
        XCTAssertNil(model.response)
        XCTAssertTrue(server.requests.isEmpty)
        server.enqueue(200, #"{"organization_id":"org_company","accepted":true}"#)
        await model.respond(accept: true)
        let accepted = server.take()
        XCTAssertEqual(accepted.url?.path, "/v1/invitations/respond")
        XCTAssertEqual(jsonBody(accepted)["accept"] as? Bool, true)
        XCTAssertNil(header(accepted, "X-Docmatch-Org"))
        XCTAssertTrue(model.response?.accepted == true)
    }
    func testWrongEmailAndExpiredLinksCannotBeAcceptedFromTheScreen() async throws {
        for json in [
            preview.replacingOccurrences(of: "true", with: "false"),
            preview.replacingOccurrences(of: "pending", with: "expired"),
        ] {
            let server = FakeServer()
            let model = InvitationModel(dm: try newClient(server), token: "link-token")
            server.enqueue(200, json)
            await model.load()
            _ = server.take()
            await model.respond(accept: true)
            XCTAssertTrue(server.requests.isEmpty)
            XCTAssertNil(model.response)
        }
    }
    func testLinksRejectOtherOriginsAmbiguousTokensAndCredentials() throws {
        let site = try XCTUnwrap(URL(string: "https://app.example.test"))
        let token = String(repeating: "a", count: 43)
        for valid in ["https://app.example.test/convite#token=", "docmatch://convite#token="] {
            XCTAssertEqual(InvitationLink.token(try XCTUnwrap(URL(string: valid + token)), site: site), token)
        }
        for invalid in [
            "https://evil.test/convite#token=", "https://me@app.example.test/convite#token=",
            "https://app.example.test:444/convite#token=", "docmatch://other#token=",
            "https://app.example.test/convite?token=",
        ] {
            XCTAssertNil(InvitationLink.token(try XCTUnwrap(URL(string: invalid + token)), site: site))
        }
        XCTAssertNil(
            InvitationLink.token(
                try XCTUnwrap(URL(string: "docmatch://convite#token=\(token)&token=\(token)")), site: site))
    }
}
