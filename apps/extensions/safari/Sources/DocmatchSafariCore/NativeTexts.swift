import Foundation

enum NativeTexts {
    static func text(_ key: String) -> String {
        guard let url = Bundle.main.url(forResource: "pt-BR", withExtension: "json"),
            let data = try? Data(contentsOf: url),
            let all = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let section = all["extensions"] as? [String: String], let value = section[key]
        else { return key }
        return value
    }
}
