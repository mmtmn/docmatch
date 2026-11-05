// Money, dates and sizes the pt-BR way, identical on every screen of the app.
import Foundation

public enum Format {
    /// Centavos to "R$ 1.234,56" (negative values keep the sign).
    public static func money(_ cents: Int64) -> String {
        let f = NumberFormatter()
        f.locale = ptBR
        f.numberStyle = .currency
        f.currencyCode = "BRL"
        return f.string(from: NSDecimalNumber(value: cents).dividing(by: 100)) ?? "\(cents)"
    }

    /// 1234 to "1.234".
    public static func integer(_ n: Int64) -> String {
        let f = NumberFormatter()
        f.locale = ptBR
        f.numberStyle = .decimal
        return f.string(from: NSNumber(value: n)) ?? "\(n)"
    }

    /// An RFC 3339 timestamp as "05/11/2026 08:16" in the given zone; "" when it does not parse.
    public static func dateTime(_ rfc3339: String?, zone: TimeZone = .current) -> String {
        guard let rfc3339, let date = parseTimestamp(rfc3339) else { return "" }
        return formatter("dd/MM/yyyy HH:mm", zone).string(from: date)
    }

    /// An ISO date (2026-07-16) as 16/07/2026; "" when it does not parse.
    public static func date(_ iso: String?) -> String {
        guard let iso, iso.count >= 10 else { return "" }
        let utc = TimeZone(identifier: "UTC")!
        guard let day = formatter("yyyy-MM-dd", utc).date(from: String(iso.prefix(10))) else { return "" }
        return formatter("dd/MM/yyyy", utc).string(from: day)
    }

    /// Bytes the site's way (apps/web/src/lib/format.ts): binary units, one decimal under 10.
    public static func size(_ texts: Texts, _ bytes: Int64) -> String {
        func scaled(_ v: Double) -> String {
            let f = NumberFormatter()
            f.locale = ptBR
            f.numberStyle = .decimal
            f.minimumFractionDigits = 0
            f.maximumFractionDigits = v < 10 ? 1 : 0
            return f.string(from: NSNumber(value: v)) ?? "\(v)"
        }
        let kb = 1024.0, mb = kb * 1024, gb = mb * 1024
        let b = Double(bytes)
        if b >= gb { return texts.t("files.size_gb", ["n": scaled(b / gb)]) }
        if b >= mb { return texts.t("files.size_mb", ["n": scaled(b / mb)]) }
        if b >= kb { return texts.t("files.size_kb", ["n": scaled(b / kb)]) }
        return texts.t("files.size_bytes", ["n": integer(bytes)])
    }

    /// An approximate duration the site's way (testdata/duration/cases.json): minutes rounded half
    /// up, at least one; under an hour, minutes ("12 minutos"); up to ten hours, hours and minutes
    /// in steps of five ("1 h 20 min"); from ten hours on, whole hours ("11 h").
    public static func duration(_ texts: Texts, _ seconds: Int64) -> String {
        let s = max(seconds, 0)
        var minutes = max(1, s / 60 + (s % 60 >= 30 ? 1 : 0))
        if minutes < 60 {
            return minutes == 1 ? texts.t("duration.minute") : texts.t("duration.minutes", ["minutes": minutes])
        }
        minutes = minutes < 600 ? (minutes + 2) / 5 * 5 : (minutes + 30) / 60 * 60
        let hours = minutes / 60
        let rest = minutes % 60
        return rest == 0
            ? texts.t("duration.hours", ["hours": hours])
            : texts.t("duration.hours_minutes", ["hours": hours, "minutes": rest])
    }

    /// The confirmation line of a large order (app.confirm_large); nil for the others.
    public static func largeNotice(_ texts: Texts, _ quote: Quote) -> String? {
        guard quote.large, let seconds = quote.expectedSeconds, seconds > 0 else { return nil }
        return texts.t("app.confirm_large", ["duration": duration(texts, seconds)])
    }

    /// The confirmation of a quote: the tokens (app.confirm_run) and, for a large order, its
    /// expected duration on a paragraph of its own.
    public static func confirmRun(_ texts: Texts, _ quote: Quote) -> String {
        let tokens = texts.t("app.confirm_run", ["expected": quote.expectedCredits, "cap": quote.capCredits])
        guard let notice = largeNotice(texts, quote) else { return tokens }
        return tokens + "\n\n" + notice
    }

    static func parseTimestamp(_ s: String) -> Date? {
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        if let d = plain.date(from: s) { return d }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: s)
    }

    private static func formatter(_ pattern: String, _ zone: TimeZone) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.calendar = Calendar(identifier: .gregorian)
        f.timeZone = zone
        f.dateFormat = pattern
        return f
    }
}
