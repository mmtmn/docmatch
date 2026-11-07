import Foundation
import XCTest

@testable import DocmatchKit

@MainActor
final class AccountSwitchTests: XCTestCase {
    private let accounts =
        #"{"user_id":"usr_1","items":[{"id":"org_personal","name":"Pessoal","kind":"personal","role":"owner","plan":"starter","capabilities":{"manage_account":true,"create_project":true,"reconcile":true,"read_results":true,"upload":true}},{"id":"org_company","name":"Empresa","kind":"team","role":"uploader","plan":"pro","capabilities":{"manage_account":false,"create_project":false,"reconcile":false,"read_results":false,"upload":true}}]}"#
    func testSelectionSurvivesRestartButRequiresSameIdentityOriginAndMembership() async throws {
        for scenario in ["restart", "identity", "origin", "membership", "logout"] {
            let server = FakeServer(), store = MemorySecretStore()
            let session = SessionModel(dm: try newClient(server), accountStore: store)
            server.enqueue(200, accounts)
            server.enqueue(200, sessionJSON)
            await session.refresh()
            server.enqueue(200, accounts)
            server.enqueue(200, sessionJSON)
            await session.selectAccount("org_company")
            XCTAssertEqual(session.accountID, "org_company")
            if scenario == "logout" {
                server.enqueue(200, "{}")
                await session.signedOut(notice: nil)
                XCTAssertNil(store.read())
            }
            let base = scenario == "origin" ? URL(string: "https://other.invalid")! : apiBase
            let reopened = SessionModel(dm: try newClient(server, base: base), accountStore: store)
            var available = accounts
            if scenario == "identity" { available = available.replacingOccurrences(of: "usr_1", with: "usr_2") }
            if scenario == "membership" { available = available.replacingOccurrences(of: "org_company", with: "org_other") }
            server.enqueue(200, available)
            server.enqueue(200, sessionJSON)
            await reopened.refresh()
            XCTAssertEqual(reopened.accountID, scenario == "restart" ? "org_company" : "org_personal", scenario)
            XCTAssertEqual(header(server.requests[server.requests.count - 2], "X-Docmatch-Org"), nil)
            XCTAssertEqual(header(server.requests.last!, "X-Docmatch-Org"), reopened.accountID)
        }
    }
    func testSelectedCompanyDoesNotChangeAnExistingClientAndRemovalReturnsToPersonal() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        let cleanups = Locked(0)
        let session = SessionModel(dm: dm, cleanup: { cleanups.mutate { $0 += 1 } })
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON)
        await session.refresh()
        XCTAssertEqual(session.accountID, "org_personal")
        let original = session.currentDM
        for _ in 0..<2 { _ = server.take() }
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON)
        await session.selectAccount("org_company")
        XCTAssertEqual(session.currentAccount?.role, "uploader")
        XCTAssertEqual(session.currentDM.api.organizationID, "org_company")
        XCTAssertEqual(original.api.organizationID, "org_personal")
        XCTAssertEqual(header(server.take(), "X-Docmatch-Org"), nil)
        XCTAssertEqual(header(server.take(), "X-Docmatch-Org"), "org_company")
        let company = session.currentDM
        let onlyPersonal = accounts.replacingOccurrences(
            of:
                #",{"id":"org_company","name":"Empresa","kind":"team","role":"uploader","plan":"pro","capabilities":{"manage_account":false,"create_project":false,"reconcile":false,"read_results":false,"upload":true}}"#,
            with: "")
        server.enqueue(200, onlyPersonal)
        server.enqueue(200, onlyPersonal)
        server.enqueue(200, sessionJSON)
        await session.validateAccounts()
        XCTAssertEqual(session.accountID, "org_personal")
        XCTAssertEqual(company.api.organizationID, "org_company")
        XCTAssertGreaterThanOrEqual(cleanups.value, 3)
    }
    func testNetworkFailureDoesNotSilentlyChangeTheSelectedCompany() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        let session = SessionModel(dm: dm)
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON)
        await session.refresh()
        server.failNetwork = true
        await session.selectAccount("org_company")
        XCTAssertEqual(session.accountID, "org_personal")
        XCTAssertNotNil(session.accountError)
        XCTAssertFalse(session.switching)
    }
    func testDelayedAccountSelectionCannotRestoreSignedOutSession() async throws {
        let server = FakeServer()
        let session = SessionModel(dm: try newClient(server))
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON)
        await session.refresh()
        server.enqueue(200, accounts)
        server.enqueue(200, sessionJSON, delayMilliseconds: 250)
        let switchTask = Task { await session.selectAccount("org_company") }
        for _ in 0..<100 where server.requests.count < 4 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(server.requests.count, 4)
        server.enqueue(200, "{}")
        await session.signedOut(notice: nil)
        await switchTask.value
        guard case .signedOut = session.state else { return XCTFail("A late response restored the session") }
        XCTAssertNil(session.accountID)
        XCTAssertTrue(session.accounts.isEmpty)
        XCTAssertFalse(session.switching)
    }

}
