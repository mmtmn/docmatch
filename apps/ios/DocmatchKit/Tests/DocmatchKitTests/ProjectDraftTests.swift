import Foundation
import XCTest
@testable import DocmatchKit

@MainActor
final class ProjectDraftTests: XCTestCase {
    private let scope = ProjectDraftScope(origin: "https://docmatch.test", userID: "usr_1", accountID: "org_1", projectID: "prj_1")

    func testReopeningAndScopeIsolation() {
        let store = MemorySecretStore()
        let journal = ProjectDraftJournal(store: store, now: { 100 })
        XCTAssertTrue(journal.editor(scope: scope).save(version: 2, name: "Local text", settings: ProjectSettings()))
        let reopened = ProjectDraftJournal(store: store, now: { 101 })
        XCTAssertEqual(reopened.editor(scope: scope).list().first?.name, "Local text")
        for other in [
            ProjectDraftScope(origin: "https://other.test", userID: "usr_1", accountID: "org_1", projectID: "prj_1"),
            ProjectDraftScope(origin: scope.origin, userID: "usr_2", accountID: "org_1", projectID: "prj_1"),
            ProjectDraftScope(origin: scope.origin, userID: "usr_1", accountID: "org_2", projectID: "prj_1"),
            ProjectDraftScope(origin: scope.origin, userID: "usr_1", accountID: "org_1", projectID: "prj_2"),
        ] {
            XCTAssertTrue(reopened.editor(scope: other).list().isEmpty)
        }
    }

    func testConcurrentWindowsCannotEraseNewerRecoveryCopy() throws {
        let journal = ProjectDraftJournal(store: MemorySecretStore())
        let first = journal.editor(scope: scope)
        let second = journal.editor(scope: scope)
        XCTAssertTrue(first.save(version: 1, name: "Earlier", settings: ProjectSettings()))
        let reviewed = try XCTUnwrap(second.list().first)
        XCTAssertTrue(first.save(version: 1, name: "Newer", settings: ProjectSettings()))
        XCTAssertFalse(second.remove(reviewed))
        XCTAssertTrue(second.save(version: 1, name: "Second window", settings: ProjectSettings()))
        XCTAssertEqual(second.list().count, 2)
        XCTAssertTrue(second.remove())
        XCTAssertEqual(first.list().first?.name, "Newer")
    }

    func testLogoutInvalidatesDelayedWritesAndReopeningHasNoCopies() {
        let store = MemorySecretStore()
        let journal = ProjectDraftJournal(store: store)
        let old = journal.editor(scope: scope)
        XCTAssertTrue(old.save(version: 1, name: "Before logout", settings: ProjectSettings()))
        XCTAssertTrue(journal.clear())
        XCTAssertFalse(old.save(version: 1, name: "Late write", settings: ProjectSettings()))
        XCTAssertTrue(old.list().isEmpty)
        XCTAssertTrue(ProjectDraftJournal(store: store).editor(scope: scope).list().isEmpty)
        XCTAssertTrue(journal.editor(scope: scope).save(version: 2, name: "Next login", settings: ProjectSettings()))
    }

    func testExpiryAndStorageLimitDoNotEvictOtherUnsavedEdits() {
        let store = MemorySecretStore()
        let journal = ProjectDraftJournal(store: store, now: { 100 })
        let editor = journal.editor(scope: scope)
        XCTAssertTrue(editor.save(version: 1, name: "Keep", settings: ProjectSettings()))
        XCTAssertFalse(editor.save(version: 1, name: String(repeating: "x", count: 70_000), settings: ProjectSettings()))
        XCTAssertEqual(editor.list().first?.name, "Keep")
        XCTAssertTrue(ProjectDraftJournal(store: store, now: { 100 + ProjectDraftJournal.ttl }).editor(scope: scope).list().isEmpty)
    }
}
