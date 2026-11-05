import Foundation
import XCTest
@testable import DocmatchKit

final class CoreTests: XCTestCase {
    func testTextsReplacePlaceholdersAndWriteNumbersThePtBRWay() throws {
        let texts = try realTexts()
        XCTAssertEqual(texts.t("mobile.run_findings", ["count": 1234]), "1.234 achado(s)")
        XCTAssertEqual(texts.t("chave.que.nao.existe"), "chave.que.nao.existe")
        XCTAssertTrue(texts.has("auth.dev_sign_in"))
        XCTAssertEqual(texts.t("mobile.files_selected", ["count": 2, "size": "1,5 KB"]), "2 arquivo(s) · 1,5 KB")
        // An unknown placeholder stays as it is.
        XCTAssertEqual(Texts.render("x"), "x")
    }

    func testFormatIsPtBR() {
        XCTAssertEqual(plain(Format.money(123_456)), "R$ 1.234,56")
        XCTAssertEqual(plain(Format.money(-5)), "-R$ 0,05")
        XCTAssertEqual(Format.date("2026-07-16"), "16/07/2026")
        XCTAssertEqual(Format.date("não é data"), "")
        XCTAssertEqual(Format.dateTime("2026-11-05T08:16:55Z", zone: TimeZone(secondsFromGMT: -3 * 3600)!), "05/11/2026 05:16")
        XCTAssertEqual(Format.dateTime("2026-11-05T08:16:55.123Z", zone: TimeZone(secondsFromGMT: 0)!), "05/11/2026 08:16")
        XCTAssertEqual(Format.size(512), "512 B")
        XCTAssertEqual(Format.size(5_430), "5,3 KB")
        XCTAssertEqual(Format.size(1_258_291), "1,2 MB")
    }

    func testFileTypesRefuseWhatTheSiteRefuses() throws {
        let texts = try realTexts()
        XCTAssertEqual(FileTypes.mime(for: "Extrato.CSV"), "text/csv")
        XCTAssertNil(FileTypes.mime(for: "foto.heic"))
        XCTAssertNil(FileTypes.mime(for: "sem-extensao"))
        XCTAssertEqual(FileTypes.problem(texts, name: "foto.heic", size: 10), texts.t("errors.file_type_unknown", ["name": "foto.heic"]))
        XCTAssertEqual(FileTypes.problem(texts, name: "a.csv", size: 0), texts.t("errors.file_empty", ["name": "a.csv"]))
        XCTAssertNotNil(FileTypes.problem(texts, name: "a.csv", size: FileTypes.maxDocumentBytes + 1))
        XCTAssertNotNil(FileTypes.problem(texts, name: String(repeating: "á", count: 128) + ".csv", size: 10), "nome acima dos bytes do limite")
        XCTAssertNil(FileTypes.problem(texts, name: "a.csv", size: 10))
    }

    func testCookiesPersistExpireAndStayOnTheirPath() {
        var now = Date(timeIntervalSince1970: 1_000_000)
        let clock = Locked(now)
        let store = MemorySecretStore()
        let jar = CookieJar(store: store) { clock.value }
        let me = URL(string: "https://api.docmatch.invalid/v1/me")!
        let refresh = URL(string: "https://api.docmatch.invalid/v1/auth/refresh")!
        jar.save(setCookie: ["dm_session=a; Path=/; Max-Age=60; HttpOnly; Secure", "dm_refresh=r; Path=/v1/auth/refresh; Max-Age=60"], from: me)
        XCTAssertEqual(jar.header(for: me), "dm_session=a")
        XCTAssertEqual(jar.header(for: refresh), "dm_refresh=r; dm_session=a", "o caminho mais longo primeiro")
        XCTAssertNil(jar.header(for: URL(string: "http://api.docmatch.invalid/v1/me")!)?.range(of: "dm_session"), "Secure só em https")
        XCTAssertNil(jar.header(for: URL(string: "https://outro.invalid/v1/me")!), "nunca para outro host")

        let reopened = CookieJar(store: store) { clock.value }
        XCTAssertTrue(reopened.hasSession())
        reopened.save(setCookie: ["dm_session=b; Path=/; Max-Age=60"], from: me)
        XCTAssertEqual(CookieJar(store: store) { clock.value }.header(for: me), "dm_session=b", "a rotação substitui, nunca duplica")

        now.addTimeInterval(120)
        clock.mutate { $0 = now }
        XCTAssertFalse(reopened.hasSession())
        XCTAssertNil(reopened.header(for: me))
        reopened.clear()
        XCTAssertNil(store.read())
    }

    func testCookieDomainsAndDeletion() {
        let jar = CookieJar(store: MemorySecretStore())
        let url = URL(string: "https://api.docmatch.invalid/v1/me")!
        jar.save(setCookie: ["shared=1; Domain=.docmatch.invalid; Path=/", "alheio=1; Domain=outro.invalid; Path=/"], from: url)
        XCTAssertEqual(jar.header(for: URL(string: "https://app.docmatch.invalid/")!), "shared=1", "Domain vale para os subdomínios")
        XCTAssertEqual(jar.header(for: url), "shared=1", "cookie de outro domínio é recusado")
        jar.save(setCookie: ["shared=; Domain=docmatch.invalid; Path=/; Max-Age=0"], from: url)
        XCTAssertNil(jar.header(for: url))
    }

    func testJoinedSetCookieHeadersSplitBackMindingExpiresDates() {
        let joined = "a=1; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT, b=2; Path=/; Max-Age=60, c=3"
        XCTAssertEqual(
            CookieJar.splitSetCookie(joined),
            ["a=1; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT", "b=2; Path=/; Max-Age=60", "c=3"])
        let parsed = CookieJar.parse(
            "a=1; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT", host: "api.docmatch.invalid", requestPath: "/", now: Date(timeIntervalSince1970: 0))
        XCTAssertEqual(parsed?.expiresAt, 1_792_567_680_000)
    }

    func testSHA256MatchesTheStandardVectors() {
        XCTAssertEqual(Reconciler.sha256Hex(Data()), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        XCTAssertEqual(Reconciler.sha256Hex(Data("abc".utf8)), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
        XCTAssertEqual(
            Reconciler.sha256Hex(Data("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq".utf8)),
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1")
        XCTAssertEqual(
            Reconciler.sha256Hex(Data(repeating: 0x61, count: 1_000)),
            "41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3")
    }

    func testTheUserAgentNamesTheDevice() {
        XCTAssertEqual(Docmatch.iosUserAgent(version: "1.2.0", model: "iPad(13;1)", system: "18.1"), "docmatch-ios/1.2.0 (iPad 13 1; iOS 18.1)")
    }
}
