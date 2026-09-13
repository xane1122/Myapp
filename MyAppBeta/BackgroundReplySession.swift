import Foundation

// Inactive in phase one. No production endpoint is contacted while the feature flag is false.
// See docs/BACKGROUND-API.md before enabling. The background session owns downloads,
// never model requests, chat POSTs, polling loops, or credential changes.
final class BackgroundReplySession: NSObject, URLSessionDownloadDelegate {
    static let shared = BackgroundReplySession()
    static var identifier: String { (Bundle.main.bundleIdentifier ?? AppConfiguration.bundleID) + ".reply-downloads.v1" }
    private var completionHandler: (() -> Void)?
    private let finishing = DispatchGroup()
    private lazy var session: URLSession = {
        let config = URLSessionConfiguration.background(withIdentifier: Self.identifier)
        config.sessionSendsLaunchEvents = true
        config.isDiscretionary = false
        config.waitsForConnectivity = true
        config.timeoutIntervalForRequest = 900
        config.timeoutIntervalForResource = 21600
        config.httpMaximumConnectionsPerHost = 2
        return URLSession(configuration: config, delegate: self, delegateQueue: .main)
    }()

    struct Ticket: Codable {
        let jobID: String
        let routeConversationID: String
        let assistant: String?
    }
    struct ResultDocument: Codable {
        let job_id: String
        let status: String
        let conversation_id: String
        let message_id: String
        let assistant: String?
    }

    func reconnect() { if AppConfiguration.backgroundResultsEnabled { _ = session } }
    func setCompletionHandler(_ handler: @escaping () -> Void) {
        guard AppConfiguration.backgroundResultsEnabled else { handler(); return }
        completionHandler = handler; _ = session
    }

    func enqueue(_ payload: [String: Any]) throws {
        guard AppConfiguration.backgroundResultsEnabled else { throw BetaError.message("后台结果接口尚未启用。") }
        guard let job = payload["job_id"] as? String, UUID(uuidString: job) != nil,
              let conversation = payload["conversation_id"],
              let rawURL = payload["result_url"] as? String, let url = URL(string: rawURL),
              AppConfiguration.isTrusted(url), url.path == "/api/native-replies/\(job)/result",
              ChatRoute(conversationID: String(describing: conversation), messageID: "1") != nil else {
            throw BetaError.message("无效的后台下载凭据。")
        }
        let ticket = Ticket(jobID: job, routeConversationID: String(describing: conversation), assistant: payload["assistant"] as? String)
        let description = String(data: try JSONEncoder().encode(ticket), encoding: .utf8)!
        session.getAllTasks { [weak self] tasks in
            guard let self, !tasks.contains(where: { $0.taskDescription == description }), tasks.count < 8 else { return }
            let task = self.session.downloadTask(with: url)
            task.taskDescription = description
            task.resume()
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        // Signed result URLs are single-purpose; never forward their token to a redirect.
        completionHandler(nil)
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        guard let response = downloadTask.response as? HTTPURLResponse, response.statusCode == 200,
              response.mimeType == "application/json", AppConfiguration.isTrusted(response.url),
              let description = downloadTask.taskDescription?.data(using: .utf8),
              let ticket = try? JSONDecoder().decode(Ticket.self, from: description),
              let bytes = try? location.resourceValues(forKeys: [.fileSizeKey]).fileSize, bytes <= 1_048_576,
              let data = try? Data(contentsOf: location),
              let result = try? JSONDecoder().decode(ResultDocument.self, from: data),
              result.job_id == ticket.jobID, result.status == "completed", result.conversation_id == ticket.routeConversationID,
              let route = ChatRoute(conversationID: result.conversation_id, messageID: result.message_id,
                                    assistant: result.assistant ?? ticket.assistant) else { return }
        finishing.enter()
        Task {
            defer { finishing.leave() }
            // Keep only non-sensitive routing IDs, never downloaded message bodies/tokens.
            let key = "beta.completedReply.\(ticket.jobID)"
            guard !UserDefaults.standard.bool(forKey: key) else { return }
            do {
                try await NotificationService.shared.schedule(route: route)
                UserDefaults.standard.set(true, forKey: key)
            } catch { /* User can open the ordinary web history when notification permission is denied. */ }
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        // Do not retry chat/model requests, and do not log token-bearing URLs.
        if error != nil { UserDefaults.standard.set(true, forKey: "beta.backgroundResultNeedsForegroundRefresh") }
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        finishing.notify(queue: .main) { [weak self] in
            let handler = self?.completionHandler; self?.completionHandler = nil; handler?()
        }
    }
}
