import Foundation
import XCTest
@testable import DocmatchKit

final class ResultsParityTests: XCTestCase {
    func testSnapshotPreservesPendingBalancesAndLegacyRuns() throws {
        var root = URL(fileURLWithPath: #filePath)
        for _ in 0..<6 { root.deleteLastPathComponent() }
        let data = try Data(contentsOf: root.appendingPathComponent("testdata/mobile/reconciliation.json"))
        let run = try JSONDecoder().decode(Run.self, from: data)
        let doc = try XCTUnwrap(run.summary?.reconciliation)
        XCTAssertEqual(doc.totals.openBalance, 6000)
        XCTAssertEqual(doc.totals.projectedOpenBalance, 4000)
        XCTAssertEqual(doc.receivables.first?.proposedAmount, 2000)
        let legacy = try JSONDecoder().decode(Run.self, from: Data(#"{"id":"old","status":"completed","summary":{}}"#.utf8))
        XCTAssertNil(legacy.summary?.reconciliation)
        XCTAssertLessThan(findingPriority("critical"), findingPriority("high"))
        XCTAssertLessThan(findingPriority("low"), findingPriority("info"))
    }
    func testPasswordLoginAfterSignOutWithoutUnusedCode() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        for _ in 0..<2 {
            server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie()])
            try await dm.api.passwordLogin(email: "ana@example.com", password: "test-only-password")
            let request = server.take()
            XCTAssertEqual(request.url?.path, "/v1/auth/password/login")
            XCTAssertEqual(jsonBody(request) as? [String: String], ["email": "ana@example.com", "password": "test-only-password"])
            XCTAssertTrue(dm.cookies.hasSession())
            server.enqueue(204)
            try await dm.api.logout()
            _ = server.take()
            XCTAssertFalse(dm.cookies.hasSession())
        }
    }
}
