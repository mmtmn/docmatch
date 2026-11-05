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

    /// Bytes as "512 B", "5,3 KB" or "1,2 MB".
    public static func size(_ bytes: Int64) -> String {
        let f = NumberFormatter()
        f.locale = ptBR
        f.numberStyle = .decimal
        f.maximumFractionDigits = 1
        f.minimumFractionDigits = 0
        if bytes < 1024 { return "\(bytes) B" }
        if bytes < 1024 * 1024 { return "\(f.string(from: NSNumber(value: Double(bytes) / 1024)) ?? "") KB" }
        return "\(f.string(from: NSNumber(value: Double(bytes) / (1024 * 1024))) ?? "") MB"
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
