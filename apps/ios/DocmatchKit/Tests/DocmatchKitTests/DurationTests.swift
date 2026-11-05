import Foundation
import XCTest
@testable import DocmatchKit

final class DurationTests: XCTestCase {
    /// testdata/duration/cases.json: the texts the site and Android give too.
    func testDurationsFollowTheSharedCases() throws {
        var root = URL(fileURLWithPath: #filePath)
        for _ in 0..<6 { root.deleteLastPathComponent() }
        let data = try Data(contentsOf: root.appendingPathComponent("testdata/duration/cases.json"))
        let cases = try XCTUnwrap((try JSONSerialization.jsonObject(with: data) as? [String: Any])?["cases"] as? [[String: Any]])
        XCTAssertGreaterThanOrEqual(cases.count, 10)
        let texts = try realTexts()
        for c in cases {
            let seconds = try XCTUnwrap((c["seconds"] as? NSNumber)?.int64Value)
            XCTAssertEqual(Format.duration(texts, seconds), c["text"] as? String, "\(seconds) s")
        }
    }

    func testTheQuoteReadsTheTimingAndToleratesItsAbsence() throws {
        let texts = try realTexts()
        let old = try JSONDecoder().decode(Quote.self, from: Data(#"{"id":"qte_1","expected_credits":12,"cap_credits":15,"signature":"v1=x"}"#.utf8))
        XCTAssertNil(old.expectedSeconds)
        XCTAssertFalse(old.large)
        XCTAssertNil(Format.largeNotice(texts, old))

        let large = try JSONDecoder().decode(
            Quote.self,
            from: Data(#"{"id":"qte_2","expected_credits":12,"cap_credits":15,"signature":"v1=x","expected_seconds":4800,"large":true}"#.utf8))
        XCTAssertEqual(large.expectedSeconds, 4800)
        XCTAssertEqual(Format.largeNotice(texts, large), "Pedido grande: deve levar cerca de 1 h 20 min. Avisamos por e-mail quando terminar.")
        XCTAssertEqual(
            Format.confirmRun(texts, large),
            "Esta conciliação deve usar cerca de 12 tokens (no máximo 15). Continuar?\n\n"
                + "Pedido grande: deve levar cerca de 1 h 20 min. Avisamos por e-mail quando terminar.")
        XCTAssertEqual(Format.confirmRun(texts, old), "Esta conciliação deve usar cerca de 12 tokens (no máximo 15). Continuar?")

        // Small orders show nothing new, even with a duration; a large flag without one neither.
        let small = try JSONDecoder().decode(
            Quote.self,
            from: Data(#"{"id":"qte_3","expected_credits":12,"cap_credits":15,"signature":"v1=x","expected_seconds":4800,"large":false}"#.utf8))
        XCTAssertNil(Format.largeNotice(texts, small))
        let noDuration = try JSONDecoder().decode(
            Quote.self, from: Data(#"{"id":"qte_4","expected_credits":12,"cap_credits":15,"signature":"v1=x","large":true}"#.utf8))
        XCTAssertNil(Format.largeNotice(texts, noDuration))
    }
}
