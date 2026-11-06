import XCTest

/// Run explicitly against `make local`; never against production.
final class ProjectFlowTests: XCTestCase {
    @MainActor func testCreateRenameAndResumeProject() throws {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(pt-BR)", "-AppleLocale", "pt_BR"]
        app.launch()
        let local = app.buttons["Entrar sem conta (ambiente local)"]
        if local.waitForExistence(timeout: 8) { local.tap() }
        let name = app.textFields["Nome do projeto"]
        XCTAssertTrue(name.waitForExistence(timeout: 30), app.debugDescription)
        let unique = "Projeto iOS \(Int(Date().timeIntervalSince1970))"
        name.tap(); name.typeText(unique)
        app.buttons["Criar projeto"].tap()
        let add = app.buttons["Adicionar documentos"]
        XCTAssertTrue(add.waitForExistence(timeout: 30), app.debugDescription)
        let saved = app.staticTexts["Salvo"]
        XCTAssertTrue(saved.waitForExistence(timeout: 10), app.debugDescription)
        name.tap()
        name.typeText(" revisado")
        let changed = unique + " revisado"
        let predicate = NSPredicate(format: "value == %@", changed)
        expectation(for: predicate, evaluatedWith: name)
        waitForExpectations(timeout: 5)
        XCTAssertTrue(saved.waitForExistence(timeout: 10))
        // A new process must recover the confirmed name from the server.
        app.terminate(); app.launch()
        let project = app.buttons.containing(.staticText, identifier: changed).firstMatch
        XCTAssertTrue(project.waitForExistence(timeout: 30), app.debugDescription)
        project.tap()
        XCTAssertTrue(add.waitForExistence(timeout: 15))
        XCTAssertEqual(name.value as? String, changed)
        let action = app.buttons["submit-run"]
        XCTAssertTrue(action.exists)
        XCTAssertFalse(action.isEnabled, "An empty project cannot reconcile")
        let position = action.frame
        app.swipeUp()
        XCTAssertTrue(action.isHittable)
        XCTAssertEqual(action.frame.minY, position.minY, accuracy: 2)
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Escolher conta")).firstMatch.exists)
        let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        shot.name = "Saved project on iOS"; shot.lifetime = .keepAlways; self.add(shot)
    }
}
