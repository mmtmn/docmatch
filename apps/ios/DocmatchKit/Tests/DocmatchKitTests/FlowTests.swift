import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import DocmatchKit

/// The screen models against a scripted API: what the SwiftUI views show, without a simulator.
final class FlowTests: XCTestCase {
    /// A small in-memory API: dev login, uploads that pass the scan after one poll, an estimate,
    /// a run that completes on the second look, its findings and feedback.
    private func localAPI() -> FakeServer {
        let server = FakeServer()
        let polls = Locked<[String: Int]>([:])
        server.route { req in
            let path = req.url?.path ?? ""
            let method = req.httpMethod ?? "GET"
            let signedIn = (req.value(forHTTPHeaderField: "Cookie") ?? "").contains("dm_session=")
            switch (method, path) {
            case ("GET", "/v1/auth/methods"): return fakeReply(200, #"{"email":true,"dev":true}"#, [])
            case ("GET", "/v1/me"):
                return signedIn
                    ? fakeReply(200, #"{"user":{"id":"usr_1","email":"dev@docmatch.test"},"org":{"id":"org_1"},"credits":{"available":10000}}"#, [])
                    : fakeReply(401, unauthenticated, [])
            case ("POST", "/v1/auth/dev/login"): return fakeReply(200, sessionJSON, [sessionCookie(), refreshCookie(), deviceCookie()])
            case ("POST", "/v1/uploads"):
                let n = polls.value["uploads", default: 0] + 1
                polls.mutate { $0["uploads"] = n }
                return fakeReply(201, #"{"id":"upl_\#(n)","method":"POST","url":"http://localhost:8080/v1/uploads/local/x","fields":{"key":"k"}}"#, [])
            case ("POST", "/v1/uploads/local/x"): return fakeReply(204, "", [])
            case ("POST", let p) where p.hasSuffix("/complete"):
                let n = polls.value[p, default: 0]
                polls.mutate { $0[p] = n + 1 }
                return fakeReply(200, #"{"id":"upl","status":"\#(n == 0 ? "scanning" : "ready")"}"#, [])
            case ("POST", "/v1/runs/estimate"): return fakeReply(200, #"{"id":"qte_1","expected_credits":120,"cap_credits":180,"signature":"sig"}"#, [])
            case ("POST", "/v1/runs"): return fakeReply(202, #"{"id":"run_1","status":"queued"}"#, [])
            case ("GET", "/v1/runs/run_1"):
                let n = polls.value["run", default: 0]
                polls.mutate { $0["run"] = n + 1 }
                return fakeReply(200, n == 0 ? #"{"id":"run_1","status":"running","progress":{"message":"Lendo"}}"# : #"{"id":"run_1","status":"completed"}"#, [])
            case ("GET", "/v1/runs/run_1/findings"):
                return fakeReply(200, #"{"items":[{"id":"fnd_small","title":"Pequeno","amount_at_risk":10},{"id":"fnd_big","title":"Grande","amount_at_risk":900}]}"#, [])
            case ("POST", let p) where p.hasSuffix("/feedback"): return fakeReply(200, #"{"status":"confirmed"}"#, [])
            default: return fakeReply(404, #"{"code":"not_found","message":"Não encontrado."}"#, [])
            }
        }
        return server
    }

    @MainActor
    func testLocalSignInThenTheWholeReconciliation() async throws {
        let server = localAPI()
        let dm = try newClient(server)
        let session = SessionModel(dm: dm)
        await session.refresh()
        guard case .signedOut(let out) = session.state else { return XCTFail("esperava o login") }
        XCTAssertTrue(out.methods.dev)
        await session.devLogin()
        guard case .signedIn(let me) = session.state else { return XCTFail("esperava a conta") }
        XCTAssertEqual(me.user.email, "dev@docmatch.test")

        let newRun = NewRunModel(dm: dm)
        let csv = Data("a;b\n1;2\n".utf8)
        await newRun.add([
            PickedFile(name: "01_contas.csv", size: 8, source: .data(csv)) { csv },
            PickedFile(name: "foto.heic", size: 8, source: .data(Data())) { Data() },
            PickedFile(name: "01_contas.csv", size: 8, source: .data(csv)) { csv },
        ])
        XCTAssertEqual(newRun.files.map(\.name), ["01_contas.csv"])
        let texts = try realTexts()
        XCTAssertEqual(
            newRun.error,
            [texts.t("errors.file_type_unknown", ["name": "foto.heic"]), texts.t("errors.file_duplicate", ["name": "01_contas.csv"])].joined(separator: "\n"))
        newRun.setRules(String(repeating: "x", count: Limits.Text.rulesText + 10))
        XCTAssertEqual(newRun.options.rulesText.unicodeScalars.count, Limits.Text.rulesText)

        await newRun.submit()
        guard case .confirm(let prepared) = newRun.phase else { return XCTFail("esperava a confirmação: \(newRun.phase)") }
        XCTAssertEqual(prepared.quote.expectedCredits, 120)
        await newRun.confirm()
        XCTAssertEqual(newRun.started, "run_1")
        XCTAssertTrue(newRun.files.isEmpty)
        XCTAssertEqual(newRun.phase, .idle)

        let run = RunModel(dm: dm, runId: "run_1")
        await run.follow()
        guard case .done(_, let findings) = run.state else { return XCTFail("esperava os achados: \(run.state)") }
        XCTAssertEqual(findings.map(\.id), ["fnd_big", "fnd_small"], "por valor em risco")
        await run.decide(findings[0], Decision.confirmed)
        guard case .done(_, let decided) = run.state else { return XCTFail() }
        XCTAssertEqual(decided[0].status, Decision.confirmed)
        XCTAssertEqual(run.notice, texts.t("mobile.finding_decided"))
    }

    @MainActor
    func testTheEmailCodeSignInAndItsErrors() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(401, unauthenticated)  // /v1/me
        server.enqueue(200, #"{"email":true}"#)  // methods
        let session = SessionModel(dm: dm)
        await session.refresh()
        server.enqueue(200, #"{"challenge_id":"chl_1","expires_at":"2026-11-05T08:30:00Z"}"#)
        await session.sendCode("  ana@example.com ")
        guard case .signedOut(let waiting) = session.state else { return XCTFail() }
        XCTAssertEqual(waiting.challengeId, "chl_1")
        XCTAssertEqual(waiting.email, "ana@example.com")
        XCTAssertFalse(waiting.busy)

        server.enqueue(400, #"{"code":"invalid_code","message":"Código inválido ou expirado."}"#)
        await session.verifyCode("000000")
        guard case .signedOut(let wrong) = session.state else { return XCTFail() }
        XCTAssertEqual(wrong.error, "Código inválido ou expirado.")
        XCTAssertEqual(wrong.challengeId, "chl_1", "o código errado não fecha o desafio")

        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie(), deviceCookie()])
        server.enqueue(200, #"{"user":{"id":"usr_1","email":"ana@example.com"},"org":{"id":"org_1"}}"#)
        await session.verifyCode("123456")
        guard case .signedIn(let me) = session.state else { return XCTFail("\(session.state)") }
        XCTAssertEqual(me.user.email, "ana@example.com")
    }

    @MainActor
    func testARevokedPhoneGoesBackToSignInWithTheAPIsMessage() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie(), deviceCookie()])
        try await dm.api.devLogin()
        server.enqueue(200, #"{"user":{"id":"usr_1","email":"ana@example.com"},"org":{"id":"org_1"}}"#)
        let session = SessionModel(dm: dm)
        await session.refresh()
        guard case .signedIn = session.state else { return XCTFail() }

        // Another device revoked this one: the history's next request is refused.
        server.enqueue(401, unauthenticated)
        server.enqueue(200, #"{"email":true}"#)
        let history = HistoryModel(dm: dm)
        await history.refresh()
        for _ in 0..<50 {
            if case .signedOut = session.state { break }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        guard case .signedOut(let out) = session.state else { return XCTFail("esperava o login: \(session.state)") }
        XCTAssertEqual(out.error, "Faça login para continuar.")
        XCTAssertFalse(dm.cookies.hasSession())
    }

    @MainActor
    func testTheAccountListsDevicesAndSigningThisOneOutReturnsToSignIn() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie(), deviceCookie()])
        try await dm.api.devLogin()
        let signedOut = Locked<[String?]>([])
        let account = AccountModel(dm: dm) { notice in signedOut.mutate { $0.append(notice) } }
        server.enqueue(200, #"{"user":{"id":"usr_1","email":"ana@example.com"},"org":{"id":"org_1"},"credits":{"available":7}}"#)
        server.enqueue(200, #"{"items":[{"id":"ses_1","platform":"ios","name":"App iOS · iPhone15,2","created_at":"2026-11-05T08:00:00Z","last_seen_at":"2026-11-05T08:10:00Z","current":true},{"id":"ses_2","platform":"android","name":"App Android · Pixel 8","created_at":"2026-11-04T08:00:00Z","last_seen_at":"2026-11-04T09:00:00Z"}]}"#)
        await account.refresh()
        guard case .ready(let me, let devices, _) = account.state else { return XCTFail("\(account.state)") }
        XCTAssertEqual(me.credits?.available, 7)
        XCTAssertEqual(devices.map(\.name), ["App iOS · iPhone15,2", "App Android · Pixel 8"])

        server.enqueue(204)
        await account.revoke(devices[0])
        XCTAssertEqual(signedOut.value, [try realTexts().t("account.signed_out")])
        XCTAssertFalse(dm.cookies.hasSession())
    }

    @MainActor
    func testHistoryPagesWithoutDuplicates() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, #"{"items":[{"id":"run_2","status":"completed"},{"id":"run_1","status":"failed"}],"next_cursor":"c1"}"#)
        let history = HistoryModel(dm: dm)
        await history.refresh()
        server.enqueue(200, #"{"items":[{"id":"run_1","status":"failed"},{"id":"run_0","status":"cancelled"}]}"#)
        await history.more()
        guard case .ready(let runs, let next, _) = history.state else { return XCTFail() }
        XCTAssertEqual(runs.map(\.id), ["run_2", "run_1", "run_0"])
        XCTAssertNil(next)
        let texts = try realTexts()
        XCTAssertEqual(Labels.runStatus(texts, runs[0].status), texts.t("mobile.status_completed"))
    }

    func testARejectedUploadSaysWhyWithTheSitesMessage() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(201, #"{"id":"upl_1","method":"POST","url":"https://uploads.docmatch.invalid/","fields":{}}"#)
        server.enqueue(204)
        server.enqueue(200, #"{"id":"upl_1","status":"rejected","rejected_reason":"macro"}"#)
        do {
            _ = try await dm.reconciler.upload(
                LocalFile(name: "a.xlsm", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", size: 1, kind: .xlsx, source: .data(Data([1]))))
            XCTFail("esperava recusa")
        } catch let e as ApiError {
            XCTAssertEqual(e.code, ApiError.uploadRejected)
            XCTAssertEqual(e.message, try realTexts().t("app.rejected", ["name": "a.xlsm", "reason": "macro"]))
        }
    }

    @MainActor
    func testAParkedRunShowsTheExtraEstimateAndResumesOnceApproved() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, #"{"id":"run_9","status":"needs_approval","pending_quote":{"id":"quo_2","expected_credits":90,"cap_credits":120,"signature":"v1=ab"}}"#)
        let run = RunModel(dm: dm, runId: "run_9")
        await run.follow()
        guard case .needsApproval(let parked, false) = run.state else { return XCTFail("\(run.state)") }
        XCTAssertEqual(parked.pendingQuote?.expectedCredits, 90)
        server.enqueue(200, #"{"id":"run_9","status":"running"}"#)
        await run.approve()
        XCTAssertEqual(run.state, .running(nil))
        XCTAssertEqual(run.attempt, 1, "a tela volta a acompanhar")
    }
}
