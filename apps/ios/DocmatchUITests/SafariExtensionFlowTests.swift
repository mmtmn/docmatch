import XCTest

/// Run explicitly against the local stack on a dedicated, English-language simulator.
/// Enable docmatch (local) first; put the book's two CSV examples in Files/Downloads.
final class SafariExtensionFlowTests: XCTestCase {
    @MainActor func testUploadReconcileExportAndResume() throws {
        continueAfterFailure = false
        // Install the freshly built extension resources before launching Safari.
        let app = XCUIApplication()
        app.launch()
        let safari = XCUIApplication(bundleIdentifier: "com.apple.mobilesafari")
        safari.terminate()
        safari.launch()
        openPopup(safari)
        if !safari.buttons["Sair da extensão"].waitForExistence(timeout: 5) {
            signIn(safari, app: app)
        }
        let open = safari.buttons["Abrir projetos"]
        XCTAssertTrue(open.waitForExistence(timeout: 15))
        reveal(open, in: safari.webViews["docmatch (local)"])
        open.tap()
        let name = "Fechamento Safari iOS \(Int(Date().timeIntervalSince1970))"
        let nameField = safari.textFields["Nome do projeto"]
        XCTAssertTrue(nameField.waitForExistence(timeout: 15))
        nameField.tap()
        nameField.typeText(name)
        safari.buttons["Criar projeto"].tap()
        let add = safari.buttons["Adicionar documentos"].firstMatch
        XCTAssertTrue(add.waitForExistence(timeout: 15))
        reveal(add, in: safari.webViews.firstMatch)
        add.tap()
        if safari.buttons["Choose Files"].waitForExistence(timeout: 3) {
            safari.buttons["Choose Files"].tap()
        }
        if !safari.cells["cobrancas, csv"].waitForExistence(timeout: 3) {
            if safari.buttons["Browse"].exists { safari.buttons["Browse"].firstMatch.tap() }
            let local = safari.cells["On My iPhone"]
            if local.waitForExistence(timeout: 3) { local.tap() }
            let downloads = safari.cells["Downloads"]
            if downloads.waitForExistence(timeout: 3) { downloads.tap() }
        }
        let charges = safari.cells["cobrancas, csv"]
        XCTAssertTrue(charges.waitForExistence(timeout: 10))
        charges.tap()
        safari.buttons["Select All"].tap()
        safari.buttons["Open"].tap()
        XCTAssertTrue(safari.staticTexts["2 de 2 arquivos prontos"].waitForExistence(timeout: 60))
        safari.buttons["Ver arquivos (2)"].tap()
        XCTAssertTrue(safari.staticTexts["cobrancas.csv"].exists)
        XCTAssertTrue(safari.staticTexts["extrato.csv"].exists)
        let reconcile = safari.buttons["Conciliar ou ver resultado"]
        XCTAssertTrue(reconcile.isHittable, "The action stays visible below the file list")
        reconcile.tap()
        let confirm = safari.buttons["Iniciar conciliação"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 15))
        confirm.tap()
        let download = safari.buttons["Baixar dados corrigidos"]
        XCTAssertTrue(download.waitForExistence(timeout: 90))
        download.tap()
        let saveFile = safari.buttons["Salvar arquivo"]
        XCTAssertTrue(saveFile.waitForExistence(timeout: 20))
        saveFile.tap()
        let saveToFiles = safari.cells["Save to Files"]
        XCTAssertTrue(saveToFiles.waitForExistence(timeout: 15))
        saveToFiles.tap()
        let save = safari.buttons["Save"]
        XCTAssertTrue(save.waitForExistence(timeout: 10))
        save.tap()
        XCTAssertTrue(download.waitForExistence(timeout: 15))
        safari.terminate()
        safari.launch()
        openPopup(safari)
        let project = safari.links[name].firstMatch
        XCTAssertTrue(project.waitForExistence(timeout: 15))
        project.tap()
        XCTAssertTrue(reconcile.waitForExistence(timeout: 15))
        reconcile.tap()
        XCTAssertTrue(download.waitForExistence(timeout: 30))
        XCTAssertFalse(confirm.exists, "Unchanged inputs reopen the result without a new quote")
        let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        shot.name = "Safari project result after restart"
        shot.lifetime = .keepAlways
        self.add(shot)
        print("SAFARI_PROJECT_QA \(name)")
    }

    @MainActor private func reveal(_ element: XCUIElement, in scroll: XCUIElement) {
        for _ in 0..<5 {
            if element.isHittable { return }
            scroll.swipeUp()
        }
        XCTAssertTrue(element.isHittable)
    }

    @MainActor private func openPopup(_ safari: XCUIApplication) {
        let address = safari.descendants(matching: .any)
            .matching(NSPredicate(format: "label == %@", "Address")).firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 10))
        address.tap()
        let field = safari.textFields["Address"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.typeText("http://localhost:14321/\n")
        XCTAssertTrue(safari.buttons["Page Menu"].waitForExistence(timeout: 10))
        safari.buttons["Page Menu"].tap()
        let item = safari.cells["docmatch (local)"]
        XCTAssertTrue(item.waitForExistence(timeout: 10), "Enable the extension on the dedicated simulator before running this test")
        item.tap()
    }

    @MainActor private func signIn(_ safari: XCUIApplication, app: XCUIApplication) {
        if safari.buttons["Cancelar"].exists { safari.buttons["Cancelar"].tap() }
        XCTAssertTrue(safari.buttons["Entrar"].waitForExistence(timeout: 5))
        safari.buttons["Entrar"].tap()
        XCTAssertTrue(safari.links["Continuar no app"].waitForExistence(timeout: 5))
        safari.links["Continuar no app"].tap()
        if safari.buttons["Open"].waitForExistence(timeout: 5) { safari.buttons["Open"].tap() }
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10))
        let labels = NSPredicate(format: "label IN %@", ["Continue", "Continuar"])
        let accept = app.buttons.matching(labels).firstMatch
        if accept.waitForExistence(timeout: 5) { accept.tap() }
        let system = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let systemAccept = system.buttons.matching(labels).firstMatch
        if systemAccept.waitForExistence(timeout: 3) { systemAccept.tap() }
        XCTAssertTrue(app.webViews.firstMatch.waitForExistence(timeout: 20))
        let signIn = app.webViews.buttons["Entrar"].firstMatch
        if signIn.waitForExistence(timeout: 5) { signIn.tap() }
        let local = app.buttons["Entrar sem conta (ambiente local)"]
        if local.waitForExistence(timeout: 10) { local.tap() }
        XCTAssertTrue(app.buttons["Conectar extensão"].waitForExistence(timeout: 20))
        app.buttons["Conectar extensão"].tap()
        XCTAssertTrue(app.staticTexts["Volte à extensão para continuar."].waitForExistence(timeout: 20))
        safari.activate()
        safari.buttons["Page Menu"].tap()
        safari.cells["docmatch (local)"].tap()
    }
}
