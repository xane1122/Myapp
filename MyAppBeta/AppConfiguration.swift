import Foundation

enum AppConfiguration {
    static let homeURL = URL(string: "https://xanelove.com")!
    static let bundleID = "com.xanelove.myapp.beta"
    static let legacyBundleID = "com.example.MyWebApp"
    // Enable only after the proposed server contract has been approved and deployed.
    static let backgroundResultsEnabled = false

    static func isTrusted(_ url: URL?) -> Bool {
        guard let url else { return false }
        return url.scheme?.lowercased() == "https" && url.host?.lowercased() == "xanelove.com"
            && (url.port == nil || url.port == 443) && url.user == nil && url.password == nil
    }
}

struct ChatRoute: Codable, Equatable {
    let conversationID: String
    let messageID: String
    let assistant: String?

    init?(conversationID: String, messageID: String, assistant: String? = nil) {
        func validID(_ value: String) -> Bool {
            !value.isEmpty && value.utf8.allSatisfy { (48...57).contains($0) }
                && (Int64(value).map { $0 > 0 && $0 <= 9_007_199_254_740_991 } ?? false)
        }
        guard validID(conversationID), validID(messageID) else { return nil }
        self.conversationID = conversationID
        self.messageID = messageID
        self.assistant = ["rhys", "grok"].contains(assistant ?? "") ? assistant : nil
    }

    init?(userInfo: [AnyHashable: Any]) {
        guard let c = userInfo["conversation_id"], let m = userInfo["message_id"] else { return nil }
        self.init(conversationID: String(describing: c), messageID: String(describing: m),
                  assistant: userInfo["assistant"] as? String)
    }

    var url: URL {
        var parts = URLComponents(url: AppConfiguration.homeURL, resolvingAgainstBaseURL: false)!
        parts.path = "/"
        parts.queryItems = [URLQueryItem(name: "conversation_id", value: conversationID),
                            URLQueryItem(name: "message_id", value: messageID),
                            URLQueryItem(name: "notification_source", value: "myapp-beta")]
        if let assistant { parts.queryItems?.append(URLQueryItem(name: "beta_assistant", value: assistant)) }
        return parts.url!
    }

    var userInfo: [String: String] {
        var result = ["conversation_id": conversationID, "message_id": messageID]
        if let assistant { result["assistant"] = assistant }
        return result
    }
}

enum BetaError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case .message(let text) = self { return text }; return nil }
}
