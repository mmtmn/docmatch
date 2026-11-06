import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import DocmatchKit

final class ProjectTests: XCTestCase {
    func testAccountAndProjectSurviveRefreshWithoutChangingAnotherClient() async throws {
        let server = FakeServer()
        let root = try newClient(server)
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie()])
        _ = try await root.api.devLogin()
        _ = server.take()
        let company = root.inAccount("org_company").inProject("prj_test")
        let personal = root.inAccount("org_personal")
        server.enqueue(401, #"{"code":"session_expired","message":"Entre de novo"}"#)
        server.enqueue(204, setCookie: [sessionCookie("s1.new")])
        server.enqueue(200, #"{"state":"unchanged","revision":4,"run":{"id":"run_1","status":"completed"}}"#)
        let result = try await company.api.reconcileProject("prj_test", revision: 4, quote: nil, idempotencyKey: "retry-same-analysis")
        XCTAssertEqual(result.run?.id, "run_1")
        XCTAssertNil(result.quote)
        let first = server.take(), refresh = server.take(), retry = server.take()
        for r in [first, retry] {
            XCTAssertEqual(header(r, "X-Docmatch-Org"), "org_company")
            XCTAssertEqual(r.url?.path, "/v1/projects/prj_test/runs")
            XCTAssertEqual(header(r, "Idempotency-Key"), "retry-same-analysis")
            XCTAssertNil(jsonBody(r)["quote_id"])
        }
        XCTAssertEqual(refresh.url?.path, "/v1/auth/refresh")
        XCTAssertNil(header(refresh, "X-Docmatch-Org"))
        server.enqueue(200, sessionJSON)
        _ = try await personal.api.me()
        XCTAssertEqual(header(server.take(), "X-Docmatch-Org"), "org_personal")
        server.enqueue(200, #"{"user_id":"usr_1","items":[]}"#)
        _ = try await company.api.organizations()
        XCTAssertNil(header(server.take(), "X-Docmatch-Org"))
    }

    func testProjectUploadKeepsDestinationAndStorageHasNoAccountCredentials() async throws {
        let server = FakeServer()
        let dm = try newClient(server).inAccount("org_company").inProject("prj_test")
        server.enqueue(200, #"{"id":"upl_1","method":"PUT","url":"https://storage.invalid/file","headers":{},"fields":{},"expires_at":"2026-12-01T00:00:00Z"}"#)
        let file = LocalFile(name: "bank.csv", mime: "text/csv", size: 4, kind: .csv, source: .data(Data("a,b\n".utf8)))
        let ticket = try await dm.api.createUpload(file, sha256: "hash")
        let create = server.take()
        XCTAssertEqual(create.url?.path, "/v1/projects/prj_test/uploads")
        XCTAssertEqual(header(create,"X-Docmatch-Org"), "org_company")
        server.enqueue(200)
        try await dm.api.putObject(ticket, file)
        let stored = server.take()
        XCTAssertEqual(stored.url?.host,"storage.invalid")
        for key in ["Cookie", "Authorization", "X-Docmatch-Org"] { XCTAssertNil(header(stored,key)) }
        server.enqueue(200, #"{"id":"upl_1","status":"ready","file_name":"bank.csv"}"#)
        _ = try await dm.api.completeUpload("upl_1",sha256:nil)
        let complete = server.take()
        XCTAssertEqual(complete.url?.path,"/v1/projects/prj_test/uploads/upl_1/complete")
        XCTAssertEqual(header(complete,"X-Docmatch-Org"), "org_company")
        server.enqueue(200, #"{"items":[],"next_cursor":null}"#)
        _ = try await dm.api.findings("run_1")
        XCTAssertEqual(server.take().url?.path, "/v1/projects/prj_test/runs/run_1/findings")
    }
}
