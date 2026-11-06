import Foundation
import XCTest

@testable import DocmatchKit

/// Optional acceptance against the real local API/worker, with public synthetic documents.
@MainActor final class LiveProjectsTests: XCTestCase {
    func testUploadsReconcilesExportsAndReusesTheSavedResult() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let raw = env["DOCMATCH_LOCAL_API"], let base = URL(string: raw) else {
            throw XCTSkip("Set DOCMATCH_LOCAL_API to the running local API")
        }
        guard base.scheme == "http", ["localhost", "127.0.0.1"].contains(base.host) else {
            return XCTFail("This test only runs against the local API")
        }
        var root = URL(fileURLWithPath: #filePath)
        for _ in 0..<6 { root.deleteLastPathComponent() }
        let dm = try Docmatch(
            baseURL: base, store: MemorySecretStore(),
            userAgent: Docmatch.iosUserAgent(version: "local-test", model: "simulator", system: "18.6"),
            textsJSON: Data(contentsOf: root.appendingPathComponent("packages/locales/pt-BR.json")), aliasLoopback: true
        )
        let methods = await dm.api.methods()
        XCTAssertTrue(methods.dev)
        guard methods.dev else { return }
        _ = try await dm.api.devLogin(email: "swift-project-\(UUID().uuidString.lowercased())@docmatch.test")
        let accounts = try await dm.api.organizations()
        let account = try XCTUnwrap(accounts.items.first { $0.kind == "personal" })
        let owner = dm.inAccount(account.id)
        let project = try await owner.api.createProject(name: "Fechamento Swift local")
        let scoped = owner.inProject(project.id)
        let names = [
            "01_contas_a_receber_protheus_jul2026.csv", "02_titulos_unidade_norte_omie_jul2026.csv",
            "03_extrato_bancario_jul2026.csv", "04_caixa_de_entrada_contas_a_pagar.txt",
        ]
        for name in names {
            let bytes = try Data(contentsOf: root.appendingPathComponent("testdata/golden/vantara-twin/" + name))
            let upload = try await scoped.reconciler.upload(LocalFile.of(name: name, bytes: bytes))
            XCTAssertEqual(upload.status, "ready")
        }
        let saved = try await owner.api.project(project.id)
        XCTAssertEqual(saved.documentCount, names.count)
        let estimate = try await owner.api.estimateProject(project.id, revision: saved.revision)
        let quote = try XCTUnwrap(estimate.quote)
        // This local-only test explicitly approves the quoted cap, with the local LLM disabled.
        let started = try await owner.api.reconcileProject(
            project.id, revision: saved.revision, quote: quote, idempotencyKey: UUID().uuidString)
        let run = try XCTUnwrap(started.run)
        let completed = try await scoped.reconciler.follow(run.id)
        XCTAssertEqual(completed.status, "completed")
        let findings = try await scoped.api.findings(run.id)
        let answer = try String(
            contentsOf: root.appendingPathComponent("testdata/golden/vantara-twin/expected.yaml"), encoding: .utf8)
        let expression = try NSRegularExpression(pattern: #"(?m)^ {2}- \{ code: F\d+"#)
        XCTAssertEqual(
            findings.count, expression.numberOfMatches(in: answer, range: NSRange(answer.startIndex..., in: answer)))
        let csv = try await scoped.api.export(runId: run.id, format: "csv")
        XCTAssertGreaterThan(csv.count, 100)
        let repeated = try await owner.api.reconcileProject(
            project.id, revision: saved.revision, quote: nil, idempotencyKey: UUID().uuidString)
        XCTAssertEqual(repeated.run?.id, run.id)
        XCTAssertNil(repeated.quote)
        let history = try await scoped.api.runs()
        XCTAssertEqual(history.items.count, 1)
        try await dm.api.logout()
    }
}
