import Foundation
import XCTest
@testable import DocmatchKit

@MainActor
final class AppleCredentialTests: XCTestCase {
    private let accounts = #"{"user_id":"usr_1","items":[{"id":"org_1","name":"Pessoal","kind":"personal","role":"owner","plan":"starter","capabilities":{"manage_account":true,"create_project":true,"reconcile":true,"read_results":true,"upload":true}}]}"#

    private func login(_ session: SessionModel, _ server: FakeServer) async {
        server.enqueue(200, "{}")
        await session.signedOut(notice: nil)
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie()])
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON)
        await session.apple(identityToken: "identity", authorizationCode: "one-use-code", fullName: nil, appleUserID: "apple-person")
        XCTAssertEqual(session.appleUserID, "apple-person")
    }

    func testRevokedCredentialClearsCookiesAndLocalDataEvenOffline() async throws {
        let server = FakeServer(), appleStore = MemorySecretStore(), accountStore = MemorySecretStore(), drafts = MemorySecretStore()
        let dm = try newClient(server)
        let session = SessionModel(dm: dm, accountStore: accountStore, draftStore: drafts, appleStore: appleStore)
        await login(session, server)
        let editor = try XCTUnwrap(session.draftEditor(projectID: "prj_test"))
        XCTAssertTrue(editor.save(version: 1, name: "Local edit", settings: ProjectSettings()))
        XCTAssertNotNil(appleStore.read())
        server.failNetwork = true
        await session.appleCredentialRevoked("apple-person")
        guard case .signedOut = session.state else { return XCTFail("revoked account stayed open") }
        XCTAssertNil(appleStore.read())
        XCTAssertNil(accountStore.read())
        XCTAssertNil(drafts.read())
        XCTAssertNil(session.accountID)
        XCTAssertNil(dm.transport.cookies.header(for: apiBase))
        XCTAssertFalse(editor.save(version: 1, name: "Late edit", settings: ProjectSettings()))
    }

    func testRememberedAppleIdentityIsBoundToOriginAndUser() async throws {
        for scenario in ["restart", "user", "origin"] {
            let server = FakeServer(), store = MemorySecretStore()
            let session = SessionModel(dm: try newClient(server), appleStore: store)
            await login(session, server)
            let base = scenario == "origin" ? URL(string: "https://other.invalid")! : apiBase
            let reopened = SessionModel(dm: try newClient(server, base: base), appleStore: store)
            server.enqueue(200, scenario == "user" ? accounts.replacingOccurrences(of: "usr_1", with: "usr_2") : accounts)
            server.enqueue(200, scenario == "user" ? sessionJSON.replacingOccurrences(of: "usr_1", with: "usr_2") : sessionJSON)
            await reopened.refresh()
            XCTAssertEqual(reopened.appleUserID, scenario == "restart" ? "apple-person" : nil)
            let count = server.requests.count
            await reopened.appleCredentialRevoked("different-apple-person")
            XCTAssertEqual(server.requests.count, count)
            guard case .signedIn = reopened.state else { return XCTFail("unrelated revocation logged out another identity") }
        }
    }
}
