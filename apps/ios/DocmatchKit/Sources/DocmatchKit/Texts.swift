// The pt-BR texts of packages/locales/pt-BR.json, the same file the web and Android use, with the
// same `{name}` placeholders and pt-BR number formatting: the app says what the site says.
import Foundation

/// pt-BR, the only language of docmatch.
public let ptBR = Locale(identifier: "pt_BR")

public final class Texts: Sendable {
    private let messages: [String: String]

    public init(json: Data) throws {
        let root = try JSONSerialization.jsonObject(with: json)
        var out: [String: String] = [:]
        Texts.flatten(root, prefix: "", into: &out)
        messages = out
    }

    /// The message for `key` with `{name}` replaced; the key itself when it is missing. Integers
    /// are written the pt-BR way (1.234).
    public func t(_ key: String, _ vars: [String: any Sendable] = [:]) -> String {
        guard let template = messages[key] else { return key }
        if vars.isEmpty { return template }
        var out = ""
        var rest = Substring(template)
        while let open = rest.firstIndex(of: "{") {
            out += rest[..<open]
            let afterOpen = rest.index(after: open)
            guard let close = rest[afterOpen...].firstIndex(of: "}") else {
                rest = rest[open...]
                break
            }
            let name = rest[afterOpen..<close]
            if !name.isEmpty, name.allSatisfy({ $0 == "_" || ("a"..."z").contains($0) }), let value = vars[String(name)] {
                out += Texts.render(value)
            } else {
                out += rest[open...close]
            }
            rest = rest[rest.index(after: close)...]
        }
        return out + rest
    }

    public func has(_ key: String) -> Bool { messages[key] != nil }

    static func render(_ value: any Sendable) -> String {
        switch value {
        case let n as Int: return Format.integer(Int64(n))
        case let n as Int64: return Format.integer(n)
        case let n as Int32: return Format.integer(Int64(n))
        default: return "\(value)"
        }
    }

    private static func flatten(_ node: Any, prefix: String, into out: inout [String: String]) {
        if let object = node as? [String: Any] {
            for (k, v) in object { flatten(v, prefix: prefix.isEmpty ? k : "\(prefix).\(k)", into: &out) }
        } else if let text = node as? String {
            out[prefix] = text
        }
        // Lists and tables are web-only.
    }
}
