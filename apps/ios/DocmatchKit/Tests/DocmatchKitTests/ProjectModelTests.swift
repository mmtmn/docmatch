import Foundation
import XCTest
@testable import DocmatchKit

@MainActor
final class ProjectModelTests: XCTestCase {
    private let project = #"{"id":"prj_test","name":"Janeiro","visibility":"private","version":1,"revision":1,"document_count":0,"created_at":"2026-11-06T10:00:00Z","updated_at":"2026-11-06T10:00:00Z","archived_at":null,"settings":{"use_rules":true,"web_search":false},"capabilities":{"manage_account":true,"create_project":true,"reconcile":true,"read_results":true,"upload":true},"documents":[]}"#
    private func loaded(_ server: FakeServer, recovery: ProjectDraftEditor? = nil) async throws -> ProjectModel {
        let model = ProjectModel(dm: try newClient(server).inAccount("org_company"), id: "prj_test", recovery: recovery)
        server.enqueue(200,project);server.enqueue(200,#"{"items":[]}"#);server.enqueue(200,#"{"items":[]}"#)
        await model.load()
        XCTAssertNotNil(model.project)
        for _ in 0..<3 { _ = server.take() }
        return model
    }
    func testConflictKeepsTheDraftAndNeverRetriesOverOtherPersonsEdit() async throws {
        let server = FakeServer(), model = try await loaded(server)
        model.rename("Minha edição")
        server.enqueue(409,#"{"code":"conflict","message":"O projeto mudou."}"#)
        let saved = await model.flush()
        XCTAssertFalse(saved)
        XCTAssertEqual(model.saveState,.conflict)
        XCTAssertEqual(model.name,"Minha edição")
        XCTAssertEqual(model.project?.name,"Janeiro")
        XCTAssertTrue(model.hasUnsavedChanges)
        let request = server.take()
        XCTAssertEqual(jsonBody(request)["version"] as? Int,1)
        XCTAssertEqual(header(request,"X-Docmatch-Org"),"org_company")
        let again = await model.flush()
        XCTAssertFalse(again)
        XCTAssertEqual(server.requests.count,0)
    }
    func testExistingResultOpensWithoutCreatingOrApprovingAnotherRun() async throws {
        let server = FakeServer(), model = try await loaded(server)
        server.enqueue(200,#"{"state":"unchanged","revision":1,"run":{"id":"run_existing","status":"completed"}}"#)
        await model.prepare()
        XCTAssertEqual(model.openedRun,"run_existing")
        XCTAssertNil(model.analysis)
        XCTAssertEqual(server.take().url?.path,"/v1/projects/prj_test/runs/estimate")
        XCTAssertEqual(server.requests.count,0)
        await model.approve()
        XCTAssertEqual(server.requests.count,0)
    }
    func testRemovedMembershipClearsCachedProjectAndResults() async throws {
        let server = FakeServer(), model = try await loaded(server)
        server.enqueue(403,#"{"code":"forbidden","message":"Você não tem acesso."}"#)
        await model.load()
        XCTAssertNil(model.project)
        XCTAssertTrue(model.runs.isEmpty)
        XCTAssertEqual(model.name,"")
    }

    private var draftScope: ProjectDraftScope {
        ProjectDraftScope(origin: apiBase.absoluteString, userID: "usr_1", accountID: "org_company", projectID: "prj_test")
    }

    func testRecoveryRequiresConfirmationAndAnotherReviewIfServerChanges() async throws {
        let journal = ProjectDraftJournal(store: MemorySecretStore())
        journal.editor(scope: draftScope).save(version: 1, name: "Recuperado", settings: ProjectSettings())
        let server = FakeServer(), model = try await loaded(server, recovery: journal.editor(scope: draftScope))
        let draft = try XCTUnwrap(model.drafts.first)
        XCTAssertEqual(model.name, "Janeiro")
        XCTAssertTrue(server.requests.isEmpty)
        let changed = project.replacingOccurrences(of: "Janeiro", with: "Colega").replacingOccurrences(of: "\"version\":1", with: "\"version\":2")
        server.enqueue(200, changed)
        await model.restoreDraft(draft)
        XCTAssertEqual(server.take().httpMethod, "GET")
        XCTAssertTrue(server.requests.isEmpty)
        XCTAssertEqual(model.name, "Colega")
        XCTAssertNotNil(model.error)
        XCTAssertEqual(model.drafts.count, 1)
        server.enqueue(200, changed)
        server.enqueue(200, changed.replacingOccurrences(of: "Colega", with: "Recuperado").replacingOccurrences(of: "\"version\":2", with: "\"version\":3"))
        await model.restoreDraft(draft)
        XCTAssertEqual(server.take().httpMethod, "GET")
        let patch = server.take()
        XCTAssertEqual(patch.httpMethod, "PATCH")
        XCTAssertEqual(jsonBody(patch)["version"] as? Int, 2)
        XCTAssertEqual(jsonBody(patch)["name"] as? String, "Recuperado")
        XCTAssertTrue(model.drafts.isEmpty)
        XCTAssertTrue(journal.editor(scope: draftScope).list().isEmpty)
        XCTAssertFalse(model.hasUnsavedChanges)
    }

    func testOfflineCopySurvivesReloadWithoutApplyingIt() async throws {
        let store = MemorySecretStore()
        let journal = ProjectDraftJournal(store: store)
        let server = FakeServer(), model = try await loaded(server, recovery: journal.editor(scope: draftScope))
        model.rename("Sem conexão")
        server.failNetwork = true
        let saved = await model.flush()
        XCTAssertFalse(saved)
        XCTAssertTrue(model.localSaved)
        XCTAssertEqual(model.saveState, .offline)
        let reopenedJournal = ProjectDraftJournal(store: store)
        let reopened = try await loaded(FakeServer(), recovery: reopenedJournal.editor(scope: draftScope))
        XCTAssertEqual(reopened.name, "Janeiro")
        XCTAssertEqual(reopened.drafts.first?.name, "Sem conexão")
        XCTAssertFalse(reopened.hasUnsavedChanges)
        model.discardPendingEdits()
    }
}
