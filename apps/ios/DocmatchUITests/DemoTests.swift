import XCTest

/// Run explicitly on the isolated recording simulator; all interactions use the real API.
final class DemoTests: XCTestCase {
    @MainActor
    func testPublicTwinRecording() throws {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(pt-BR)", "-AppleLocale", "pt_BR"]
        app.launch()
        let local = app.buttons["Entrar sem conta (ambiente local)"]
        if local.waitForExistence(timeout: 8) { local.tap() }
        let picker = app.buttons["pick-files"]
        XCTAssertTrue(picker.waitForExistence(timeout: 30), app.debugDescription)
        print("DEMO_VISIBLE \(Date().timeIntervalSince1970)")
        sleep(2)
        picker.tap()
        let folder = app.staticTexts["docmatch-public-twin"]
        if !folder.waitForExistence(timeout: 3) {
            let browse = app.buttons.matching(NSPredicate(format: "label IN %@", ["Browse", "Explorar"])).firstMatch
            XCTAssertTrue(browse.waitForExistence(timeout: 30), app.debugDescription)
            browse.tap()
            let localFiles = app.staticTexts.matching(NSPredicate(format: "label IN %@", ["On My iPhone", "No Meu iPhone", "No meu iPhone"])).firstMatch
            if localFiles.waitForExistence(timeout: 15) { localFiles.tap() }
        }
        XCTAssertTrue(folder.waitForExistence(timeout: 10), app.debugDescription)
        folder.tap()
        let names = ["01_contas_a_receber_protheus_jul2026", "02_titulos_unidade_norte_omie_jul2026", "03_extrato_bancario_jul2026", "04_caixa_de_entrada_contas_a_pagar"]
        for name in names {
            let file = app.cells.matching(NSPredicate(format: "label CONTAINS %@", name)).firstMatch
            XCTAssertTrue(file.waitForExistence(timeout: 5), app.debugDescription)
            file.tap()
        }
        let open = app.buttons.matching(NSPredicate(format: "label IN %@", ["Open", "Abrir"])).firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 5), app.debugDescription)
        open.tap()
        sleep(2)
        let submit = app.buttons["submit-run"]
        XCTAssertTrue(submit.waitForExistence(timeout: 10))
        XCTAssertTrue(submit.isEnabled, app.debugDescription)
        submit.tap()
        let confirm = app.staticTexts["Confirmar a conciliação"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 90), app.debugDescription)
        sleep(2)
        app.buttons["confirm-run"].tap()
        let download = app.buttons["Baixar dados corrigidos"]
        XCTAssertTrue(download.waitForExistence(timeout: 180), app.debugDescription)
        sleep(3)
        app.swipeUp()
        sleep(2)
        app.swipeUp()
        let finding = app.buttons["finding-row"].firstMatch
        if finding.exists && finding.isHittable {
            finding.tap()
            sleep(3)
            app.buttons["Fechar"].tap()
        }
        sleep(2)
    }

    @MainActor
    func testGoogleHandoff() throws {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(pt-BR)", "-AppleLocale", "pt_BR"]
        addUIInterruptionMonitor(withDescription: "Google sign-in permission") { alert in
            let button = alert.buttons.matching(NSPredicate(format: "label IN %@", ["Continue", "Continuar"])).firstMatch
            guard button.exists else { return false }
            button.tap()
            return true
        }
        app.launch()
        let google = app.buttons["Entrar com Google"]
        XCTAssertTrue(google.waitForExistence(timeout: 30), app.debugDescription)
        google.tap()
        let browser = XCUIApplication(bundleIdentifier: "com.apple.SafariViewService")
        let consent = browser.buttons.matching(NSPredicate(format: "label IN %@", ["Continue", "Continuar"])).firstMatch
        if consent.waitForExistence(timeout: 5) { consent.tap() }
        else { app.tap() }
        XCTAssertFalse(consent.exists, "Google permission must be accepted before inspecting the web page")
        let email = browser.webViews.textFields.firstMatch
        let loaded = email.waitForExistence(timeout: 20) || app.webViews.textFields.firstMatch.waitForExistence(timeout: 20)
        let capture = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        capture.name = "Google authorization handoff"
        capture.lifetime = .keepAlways
        add(capture)
        XCTAssertTrue(loaded, "Expected Google's email entry page after system consent")
    }
}
