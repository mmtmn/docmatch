import Foundation
import XCTest

@testable import DocmatchSafariCore

final class HandoffTests: XCTestCase {
    let state = String(repeating: "s", count: 43)
    let challenge = String(repeating: "c", count: 43)
    let code = String(repeating: "d", count: 43)
    let now = Date(timeIntervalSince1970: 2_000_000_000)
    let callback = URL(string: "docmatch-extension-dev://auth")!
    func withStore(_ test: (HandoffStore) throws -> Void) throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        try test(HandoffStore(directory: dir, callbackURL: callback))
    }
    func testSurvivesRestartAndKeepsProfilesSeparate() throws {
        try withStore { store in
            let pending = try store.begin(profile: "A", state: state, challenge: challenge, now: now)
            let restarted = try HandoffStore(directory: store.directory, callbackURL: callback)
            XCTAssertThrowsError(try restarted.poll(pending.id, profile: "B", now: now))
            XCTAssertThrowsError(try restarted.cancel(pending.id, profile: "B", now: now))
            try restarted.complete(
                pending.id, callback: URL(string: "\(callback)?state=\(state)&code=\(code)"), now: now)
            XCTAssertEqual(try store.poll(pending.id, profile: "A", now: now).code, code)
            try store.cancel(pending.id, profile: "A", now: now)
            XCTAssertThrowsError(try restarted.poll(pending.id, profile: "A", now: now))
        }
    }
    func testInvalidCallbackCannotCompleteOrCancelAnotherRequest() throws {
        try withStore { store in
            let pending = try store.begin(profile: "A", state: state, challenge: challenge, now: now)
            for url in [
                "docmatch-extension-dev://auth:42?state=\(state)&code=\(code)",
                "docmatch-extension-dev://auth?state=bad&code=\(code)",
                "docmatch-extension-dev://auth?state=\(state)&state=\(state)&code=\(code)",
                "https://auth?state=\(state)&code=\(code)",
                "docmatch-extension-dev://user@auth?state=\(state)&code=\(code)",
            ] {
                XCTAssertThrowsError(try store.complete(pending.id, callback: URL(string: url), now: now))
            }
            let result = try store.poll(pending.id, profile: "A", now: now)
            XCTAssertNil(result.code)
            XCTAssertNil(result.error)
        }
    }
    func testExpiryDuplicateCodeAndBoundedPendingRequests() throws {
        try withStore { store in
            let pending = try store.begin(profile: "A", state: state, challenge: challenge, now: now)
            XCTAssertThrowsError(try store.pending(pending.id, now: now.addingTimeInterval(600)))
            try store.complete(pending.id, callback: URL(string: "\(callback)?state=\(state)&code=\(code)"), now: now)
            XCTAssertThrowsError(try store.poll(pending.id, profile: "A", now: now.addingTimeInterval(120)))
            for _ in 0..<9 { _ = try store.begin(profile: "A", state: state, challenge: challenge, now: now) }
            XCTAssertThrowsError(try store.begin(profile: "A", state: state, challenge: challenge, now: now))
            XCTAssertThrowsError(try store.forState(state, now: now))
            // Expired files are cleaned on the next authorization, without a new background service.
            _ = try store.begin(profile: "A", state: state, challenge: challenge, now: now.addingTimeInterval(601))
            XCTAssertEqual(
                try FileManager.default.contentsOfDirectory(at: store.directory, includingPropertiesForKeys: nil).filter
                { $0.pathExtension == "json" }.count, 1)
        }
    }
    func testAppLinkHasNoCallerControlledWebsiteOrRedirect() throws {
        let config = NativeConfiguration(
            siteURL: URL(string: "http://localhost:14321")!, clientID: "safari-local", redirectURI: callback,
            local: true)
        try config.validate()
        let id = UUID()
        XCTAssertEqual(try config.requestID(URL(string: "docmatch-extension-dev://authorize?request=\(id)")!), id)
        for suffix in ["&url=https://evil.example", "&request=\(id)", "#fragment"] {
            XCTAssertThrowsError(
                try config.requestID(URL(string: "docmatch-extension-dev://authorize?request=\(id)\(suffix)")!))
        }
        XCTAssertThrowsError(
            try NativeConfiguration(
                siteURL: URL(string: "http://example.com")!, clientID: "x", redirectURI: callback, local: true
            ).validate())
    }
}
