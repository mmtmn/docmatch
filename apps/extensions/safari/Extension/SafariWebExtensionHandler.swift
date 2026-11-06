import SafariServices

final class SafariWebExtensionHandler: NSObject, NSExtensionRequestHandling {
    func beginRequest(with context: NSExtensionContext) {
        var result: [String: String] = ["error": "invalid_request"]
        defer {
            let response = NSExtensionItem()
            response.userInfo = [SFExtensionMessageKey: result]
            context.completeRequest(returningItems: [response])
        }
        guard let item = context.inputItems.first as? NSExtensionItem,
            let message = item.userInfo?[SFExtensionMessageKey] as? [String: String]
        else { return }
        // Safari supplies this identity; JavaScript cannot choose another profile.
        let profile = (item.userInfo?[SFExtensionProfileKey] as? UUID)?.uuidString ?? "default"
        do {
            let config = try NativeConfiguration.load()
            let store = try config.store()
            if message["action"] == "begin", let state = message["state"], let challenge = message["challenge"] {
                let handoff = try store.begin(profile: profile, state: state, challenge: challenge)
                var link = URLComponents(url: config.redirectURI, resolvingAgainstBaseURL: false)!
                link.host = "authorize"
                link.queryItems = [URLQueryItem(name: "request", value: handoff.id.uuidString)]
                result = ["id": handoff.id.uuidString, "openURL": link.url!.absoluteString]
            } else if let value = message["id"], let id = UUID(uuidString: value) {
                if message["action"] == "poll" {
                    let handoff = try store.poll(id, profile: profile)
                    result = ["status": "pending", "state": handoff.state]
                    if let code = handoff.code {
                        result["status"] = "ready"
                        result["code"] = code
                    }
                    if let error = handoff.error {
                        result["status"] = "error"
                        result["error"] = error
                    }
                } else if message["action"] == "cancel" {
                    try store.cancel(id, profile: profile)
                    result = ["status": "cancelled"]
                }
            }
        } catch { result = ["error": "handoff_unavailable"] }
    }
}
