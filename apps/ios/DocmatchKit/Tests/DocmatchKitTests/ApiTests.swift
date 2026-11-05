import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import DocmatchKit

final class ApiTests: XCTestCase {
    func testSignInStoresTheDeviceBoundCookiesAndTheNextLaunchReusesThem() async throws {
        let server = FakeServer()
        let store = MemorySecretStore()
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie(), deviceCookie()])
        try await newClient(server, store: store).api.emailVerify(challengeId: "chl_1", code: "123456")
        let verify = server.take()
        XCTAssertEqual(verify.url?.path, "/v1/auth/email/verify")
        XCTAssertEqual(jsonBody(verify) as? [String: String], ["challenge_id": "chl_1", "code": "123456"])
        XCTAssertEqual(header(verify, "User-Agent"), "docmatch-ios/1.0.0 (iPhone15,2; iOS 18.1)")

        // A new launch (same Keychain item) presents the same session and device.
        server.enqueue(200, #"{"user":{"id":"usr_1","email":"ana@example.com"},"org":{"id":"org_1"},"credits":{"balance":10,"reserved":0,"available":10}}"#)
        let me = try await newClient(server, store: store).api.me()
        XCTAssertEqual(me?.user.email, "ana@example.com")
        XCTAssertEqual(me?.credits?.available, 10)
        let cookie = header(server.take(), "Cookie") ?? ""
        XCTAssertTrue(cookie.contains("dm_session=s1.secret") && cookie.contains("dm_device=dev-1"), cookie)
        XCTAssertFalse(cookie.contains("dm_refresh"), "o refresh só vai para /v1/auth/refresh")
    }

    func testAnExpiredAccessWindowIsRefreshedOnceAndTheCallRetried() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        server.enqueue(401, #"{"code":"session_expired","message":"Sua sessão expirou. Entre de novo."}"#)
        server.enqueue(204, setCookie: [sessionCookie("s1.rotated")])
        server.enqueue(200, #"{"items":[]}"#)
        let devices = try await dm.api.devices()
        XCTAssertEqual(devices, [])
        XCTAssertEqual(server.take().url?.path, "/v1/me/sessions")
        XCTAssertEqual(server.take().url?.path, "/v1/auth/refresh")
        XCTAssertTrue((header(server.take(), "Cookie") ?? "").contains("s1.rotated"))
    }

    func testErrorsKeepTheServerCodeAndItsPtBRMessage() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(402, #"{"code":"insufficient_credits","message":"Saldo insuficiente.","request_id":"r1"}"#)
        do {
            _ = try await dm.api.estimate(uploadIds: ["upl_1"], options: RunOptions())
            XCTFail("esperava erro")
        } catch let e as ApiError {
            XCTAssertEqual(e, ApiError(status: 402, code: "insufficient_credits", message: "Saldo insuficiente.", requestId: "r1"))
        }
        server.enqueue(502, "")
        do {
            _ = try await dm.api.estimate(uploadIds: ["upl_1"], options: RunOptions())
            XCTFail("esperava erro")
        } catch let e as ApiError {
            XCTAssertEqual(e.code, "server")
            XCTAssertTrue(e.message.contains("502"), e.message)
        }
    }

    func testADeadNetworkSaysSoInPtBR() async throws {
        let server = FakeServer()
        server.failNetwork = true
        let dm = try newClient(server)
        do {
            _ = try await dm.api.runs()
            XCTFail("esperava erro")
        } catch let e as ApiError {
            XCTAssertEqual(e.code, ApiError.network)
            XCTAssertEqual(e.message, try realTexts().t("errors.network"))
        }
        let methods = await dm.api.methods()
        XCTAssertFalse(methods.any, "sem rede, nenhum método")
    }

    private func signedIn(_ server: FakeServer, _ dm: Docmatch) async throws {
        server.enqueue(200, sessionJSON, setCookie: [sessionCookie(), refreshCookie(), deviceCookie()])
        try await dm.api.devLogin()
        _ = server.take()
        XCTAssertTrue(dm.cookies.hasSession())
    }

    func testASessionRevokedOnAnotherDeviceIsForgottenHereAndAnnounced() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        let announced = Locked<[String]>([])
        dm.transport.onSessionEnded { e in announced.mutate { $0.append(e.message) } }
        server.enqueue(401, unauthenticated)
        do {
            _ = try await dm.api.runs()
            XCTFail("esperava erro")
        } catch let e as ApiError {
            XCTAssertEqual(e.code, "unauthenticated")
        }
        XCTAssertFalse(dm.cookies.hasSession(), "os cookies da sessão encerrada saem do aparelho")
        XCTAssertEqual(announced.value, ["Faça login para continuar."])
        XCTAssertEqual(server.requests.count, 1, "sem refresh: a sessão não existe mais")
    }

    func testARefusedRefreshEndsTheSessionEvenWhenItClearsTheCookies() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        let announced = Locked<[String]>([])
        dm.transport.onSessionEnded { e in announced.mutate { $0.append(e.code) } }
        server.enqueue(401, #"{"code":"session_expired","message":"Sua sessão expirou. Entre de novo."}"#)
        // The API clears both cookies in the refusal itself (session.Manager.Clear).
        server.enqueue(401, unauthenticated, setCookie: ["dm_session=; Path=/; Max-Age=0", "dm_refresh=; Path=/v1/auth/refresh; Max-Age=0"])
        do {
            _ = try await dm.api.devices()
            XCTFail("esperava erro")
        } catch {}
        XCTAssertEqual(server.take().url?.path, "/v1/me/sessions")
        XCTAssertEqual(server.take().url?.path, "/v1/auth/refresh")
        XCTAssertFalse(dm.cookies.hasSession())
        XCTAssertEqual(announced.value, ["unauthenticated"])
    }

    func testADeviceThatWasNeverSignedInAnnouncesNothing() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        let announced = Locked<Int>(0)
        dm.transport.onSessionEnded { _ in announced.mutate { $0 += 1 } }
        server.enqueue(401, unauthenticated)
        let me = try await dm.api.me()
        XCTAssertNil(me)
        XCTAssertEqual(announced.value, 0)
    }

    func testCallsInFlightWhenTheAccessWindowClosesRefreshOnceAndKeepTheSession() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        let refreshes = Locked(0)
        server.route { req in
            if req.url?.path == "/v1/auth/refresh" {
                refreshes.mutate { $0 += 1 }
                Thread.sleep(forTimeInterval: 0.15)  // the others' 401s arrive while this rotates
                return FakeServer.Reply(status: 204, body: Data(), headers: ["Set-Cookie": sessionCookie("s1.rotated")])
            }
            if (req.value(forHTTPHeaderField: "Cookie") ?? "").contains("s1.rotated") { return fakeReply(200, #"{"items":[]}"#) }
            return fakeReply(401, #"{"code":"session_expired","message":"Sua sessão expirou. Entre de novo."}"#)
        }
        let results = try await withThrowingTaskGroup(of: [Device].self) { group in
            for _ in 0..<4 { group.addTask { try await dm.api.devices() } }
            var out: [[Device]] = []
            for try await r in group { out.append(r) }
            return out
        }
        XCTAssertEqual(results, Array(repeating: [], count: 4))
        XCTAssertEqual(refreshes.value, 1, "uma renovação só: a segunda com o segredo antigo derrubaria a sessão")
        XCTAssertEqual(dm.cookies.sessionValue(), "s1.rotated")
    }

    func testA401ForASessionAnotherCallAlreadyRotatedRetriesInsteadOfSigningOut() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        let announced = Locked(0)
        dm.transport.onSessionEnded { _ in announced.mutate { $0 += 1 } }
        server.route { req in
            if (req.value(forHTTPHeaderField: "Cookie") ?? "").contains("s1.secret") {
                // Meanwhile another call refreshed: the old access secret is refused.
                dm.cookies.save(setCookie: [sessionCookie("s1.rotated")], from: apiBase)
                return fakeReply(401, unauthenticated)
            }
            return fakeReply(200, #"{"items":[]}"#)
        }
        let devices = try await dm.api.devices()
        XCTAssertEqual(devices, [])
        XCTAssertTrue(dm.cookies.hasSession(), "a sessão nova continua")
        XCTAssertEqual(announced.value, 0, "nada de sair: outra chamada já renovou")
    }

    func testARunWaitingForApprovalStopsThePollingAndIsApprovedOnce() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, #"{"id":"run_9","status":"running"}"#)
        server.enqueue(200, #"{"id":"run_9","status":"needs_approval","pending_quote":{"id":"quo_2","expected_credits":90,"cap_credits":120,"signature":"v1=ab"}}"#)
        let parked = try await dm.reconciler.follow("run_9")
        XCTAssertEqual(parked.status, RunStatus.needsApproval)
        XCTAssertEqual(parked.pendingQuote?.capCredits, 120)
        _ = server.take()
        _ = server.take()
        server.enqueue(200, #"{"id":"run_9","status":"running"}"#)
        _ = try await dm.api.approveQuote(runId: "run_9", quote: XCTUnwrap(parked.pendingQuote), idempotencyKey: "key-1")
        let approve = server.take()
        XCTAssertEqual(approve.url?.path, "/v1/runs/run_9/approve-quote")
        XCTAssertEqual(header(approve, "Idempotency-Key"), "key-1")
        XCTAssertEqual(jsonBody(approve) as? [String: String], ["quote_id": "quo_2", "quote_signature": "v1=ab"])
    }

    func testConfirmingTheSameEstimateTwiceSendsTheSameKey() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        let quote = try JSONDecoder().decode(Quote.self, from: Data(#"{"id":"quo_1","expected_credits":40,"cap_credits":50,"signature":"sig"}"#.utf8))
        let prepared = Prepared(uploadIds: ["upl_1"], quote: quote, options: RunOptions())
        server.enqueue(504)
        do {
            _ = try await dm.reconciler.start(prepared)
            XCTFail("esperava erro")
        } catch {}
        server.enqueue(202, #"{"id":"run_1","status":"queued"}"#)
        _ = try await dm.reconciler.start(prepared)
        let first = header(server.take(), "Idempotency-Key")
        XCTAssertEqual(first, header(server.take(), "Idempotency-Key"), "um orçamento aceito vira uma execução só")
    }

    func testDeletingTheAccountSchedulesItAndSignsThisDeviceOut() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        server.enqueue(202, #"{"scheduled_for":"2026-11-12T10:00:00Z"}"#, setCookie: ["dm_session=; Path=/; Max-Age=0", "dm_refresh=; Path=/v1/auth/refresh; Max-Age=0"])
        let when = try await dm.api.deleteAccount()
        XCTAssertEqual(when, "2026-11-12T10:00:00Z")
        let req = server.take()
        XCTAssertEqual(req.httpMethod, "DELETE")
        XCTAssertEqual(req.url?.path, "/v1/me")
        XCTAssertFalse(dm.cookies.hasSession())
    }

    func testDebugBuildsReachALoopbackUploadURLThroughTheAPIHost() throws {
        let server = FakeServer()
        let local = "http://localhost:8080/v1/uploads/local/upl_1?sig=abc"
        let lan = try newClient(server, base: URL(string: "http://lan.docmatch.invalid:8080")!, alias: true)
        let aliased = try lan.transport.presignedURL(local)
        XCTAssertEqual(aliased.host, "lan.docmatch.invalid")
        XCTAssertEqual(aliased.port, 8080)
        XCTAssertEqual(aliased.path, "/v1/uploads/local/upl_1")
        XCTAssertEqual(aliased.query, "sig=abc")
        // Release builds, and an API on the loopback itself (the simulator), keep the URL as signed.
        XCTAssertEqual(try newClient(server, base: URL(string: "http://lan.docmatch.invalid:8080")!).transport.presignedURL(local).host, "localhost")
        XCTAssertEqual(try newClient(server, base: URL(string: "http://localhost:8080")!, alias: true).transport.presignedURL(local).host, "localhost")
        let s3 = "https://uploads.docmatch.invalid/key?X-Amz-Signature=1"
        XCTAssertEqual(try lan.transport.presignedURL(s3).absoluteString, s3)
    }

    func testFormUploadsSendThePolicyFieldsFirstTheFileLastAndNoCookie() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        let ticket = try JSONDecoder().decode(
            UploadTicket.self,
            from: Data(#"{"id":"upl_1","method":"POST","url":"https://uploads.docmatch.invalid/","fields":{"key":"k/1","success_action_status":"201"}}"#.utf8))
        XCTAssertEqual(ticket.fields["success_action_status"], "201", "campos do S3 chegam como assinados")
        server.enqueue(204)
        try await dm.api.putObject(ticket, LocalFile.of(name: "extrato \"julho\".csv", bytes: Data("a;b\n1;2\n".utf8)))
        let put = server.take()
        XCTAssertEqual(put.httpMethod, "POST")
        XCTAssertNil(header(put, "Cookie"), "link pré-assinado nunca leva cookie")
        let body = String(decoding: put.httpBody ?? Data(), as: UTF8.self)
        let key = try XCTUnwrap(body.range(of: "name=\"key\""))
        let status = try XCTUnwrap(body.range(of: "name=\"success_action_status\""))
        let file = try XCTUnwrap(body.range(of: "name=\"file\"; filename=\"extrato %22julho%22.csv\""))
        XCTAssertTrue(key.lowerBound < file.lowerBound && status.lowerBound < file.lowerBound)
        XCTAssertTrue(body.contains("Content-Type: text/csv\r\n\r\na;b\n1;2\n\r\n"))
        XCTAssertTrue((header(put, "Content-Type") ?? "").hasPrefix("multipart/form-data; boundary="))

        let media = try JSONDecoder().decode(
            UploadTicket.self,
            from: Data(#"{"id":"upl_2","method":"PUT","url":"https://uploads.docmatch.invalid/m","headers":{"x-amz-checksum-sha256":"abc"}}"#.utf8))
        server.enqueue(200)
        try await dm.api.putObject(media, LocalFile(name: "nota.mp3", mime: "audio/mpeg", size: 3, kind: .audio, source: .data(Data([1, 2, 3]))))
        let raw = server.take()
        XCTAssertEqual(raw.httpMethod, "PUT")
        XCTAssertEqual(header(raw, "x-amz-checksum-sha256"), "abc")
        XCTAssertEqual(raw.httpBody, Data([1, 2, 3]))
    }

    func testRunsAreEstimatedAndStartedWithTheSignedQuoteOnce() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, #"{"id":"qte_1","expected_credits":120,"cap_credits":180,"signature":"sig"}"#)
        let quote = try await dm.api.estimate(uploadIds: ["upl_1", "upl_2"], options: RunOptions(rulesText: "  só julho  ", webSearch: true))
        let estimate = jsonBody(server.take())
        XCTAssertEqual((estimate["sources"] as? [[String: String]])?.compactMap { $0["upload_id"] }, ["upl_1", "upl_2"])
        XCTAssertEqual(estimate["rules_text"] as? String, "só julho")
        XCTAssertEqual(estimate["options"] as? [String: Bool], ["use_rules": true, "web_search": true])

        server.enqueue(202, #"{"id":"run_1","status":"queued"}"#)
        let run = try await dm.api.createRun(quote, uploadIds: ["upl_1"], options: RunOptions(), idempotencyKey: "key-1")
        XCTAssertEqual(run.id, "run_1")
        let create = server.take()
        XCTAssertEqual(header(create, "Idempotency-Key"), "key-1")
        XCTAssertEqual(jsonBody(create)["quote_id"] as? String, "qte_1")
        XCTAssertEqual(jsonBody(create)["quote_signature"] as? String, "sig")
        XCTAssertNil(jsonBody(create)["rules_text"])
    }

    func testFindingsArePagedToTheEnd() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        server.enqueue(200, #"{"items":[{"id":"fnd_1","title":"A","amount_at_risk":100}],"next_cursor":"c2"}"#)
        server.enqueue(200, #"{"items":[{"id":"fnd_2","title":"B","status":"confirmed"}]}"#)
        let findings = try await dm.api.findings("run_1")
        XCTAssertEqual(findings.map(\.id), ["fnd_1", "fnd_2"])
        XCTAssertEqual(findings[0].amountAtRisk, 100)
        XCTAssertEqual(findings[0].status, "open")
        XCTAssertEqual(findings[1].status, Decision.confirmed)
        XCTAssertNil(server.take().url?.query?.range(of: "cursor"))
        XCTAssertEqual(server.take().url?.query?.contains("cursor=c2"), true)
    }

    func testRevokingThisDeviceClearsItsCookies() async throws {
        let server = FakeServer()
        let dm = try newClient(server)
        try await signedIn(server, dm)
        server.enqueue(200, #"{"items":[{"id":"ses_1","platform":"ios","name":"App iOS · iPhone15,2","created_at":"2026-11-05T08:00:00Z","last_seen_at":"2026-11-05T08:10:00Z","current":true},{"id":"ses_2","platform":"web","name":"Firefox · Linux","created_at":"2026-11-04T08:00:00Z","last_seen_at":"2026-11-04T08:00:00Z"}]}"#)
        let devices = try await dm.api.devices()
        XCTAssertEqual(devices.map(\.platform), ["ios", "web"])
        server.enqueue(204)
        try await dm.api.revokeDevice(devices[1])
        XCTAssertEqual(server.requests.last?.url?.path, "/v1/me/sessions/ses_2")
        XCTAssertTrue(dm.cookies.hasSession(), "encerrar outro aparelho não sai deste")
        server.enqueue(204)
        try await dm.api.revokeDevice(devices[0])
        XCTAssertFalse(dm.cookies.hasSession())
    }
}
