import SwiftUI
import WebKit
import Network
import UIKit

@MainActor
final class WebAppModel: NSObject, ObservableObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    @Published var loading = true
    @Published var errorText: String?
    @Published var online = true
    @Published var status = ""
    let webView: WKWebView
    private var cookies: CookiePersistence!
    private let monitor = NWPathMonitor()
    private let media = MediaPicker()
    private var started = false
    private var ready = false
    private var failed = false
    private var lastGET = AppConfiguration.homeURL
    private var routeInFlight: ChatRoute?

    override init() {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default()
        config.allowsInlineMediaPlayback = true
        config.applicationNameForUserAgent = "MyAppBeta/0.1"
        if let sourceURL = Bundle.main.url(forResource: "NativeBridge", withExtension: "js"),
           let source = try? String(contentsOf: sourceURL, encoding: .utf8) {
            config.userContentController.addUserScript(WKUserScript(source: source, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        }
        webView = WKWebView(frame: .zero, configuration: config)
        super.init()
        config.userContentController.add(WeakScriptHandler(self), name: "myappBeta")
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.allowsBackForwardNavigationGestures = true
        webView.scrollView.keyboardDismissMode = .interactive
        webView.scrollView.contentInsetAdjustmentBehavior = .never
        webView.isOpaque = false
        webView.backgroundColor = .systemBackground
        cookies = CookiePersistence(store: config.websiteDataStore.httpCookieStore)
        monitor.pathUpdateHandler = { [weak self] path in
            DispatchQueue.main.async {
                guard let self else { return }
                let wasOffline = !self.online
                self.online = path.status == .satisfied
                if wasOffline && self.online { self.recoverIfNeeded() }
            }
        }
        monitor.start(queue: DispatchQueue(label: "com.xanelove.myapp.beta.network"))
    }

    deinit { monitor.cancel() }

    func start() {
        guard !started else { return }; started = true
        Task {
            await cookies.restore()
            ready = true
            if NotificationService.shared.pendingRoute() != nil { consumePendingRoute() }
            else { webView.load(URLRequest(url: AppConfiguration.homeURL)) }
        }
    }

    func persistCookies() { cookies.save() }

    func consumePendingRoute() {
        guard ready, let route = NotificationService.shared.pendingRoute(), routeInFlight != route else { return }
        routeInFlight = route
        webView.load(URLRequest(url: route.url))
    }

    func openDeepLink(_ url: URL) {
        guard url.scheme == "myapp-beta", url.host == "chat",
              let parts = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return }
        func value(_ name: String) -> String { parts.queryItems?.first(where: { $0.name == name })?.value ?? "" }
        guard let route = ChatRoute(conversationID: value("conversation_id"), messageID: value("message_id"), assistant: value("assistant")) else { return }
        NotificationService.shared.store(route)
        consumePendingRoute()
    }

    func recoverIfNeeded() {
        guard ready, online, failed else { return }
        failed = false
        if let route = NotificationService.shared.pendingRoute() { routeInFlight = nil; NotificationService.shared.store(route); consumePendingRoute() }
        else { webView.load(URLRequest(url: lastGET)) }
    }

    func reload() {
        if failed { recoverIfNeeded() } else { webView.reload() }
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        loading = true; errorText = nil
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        loading = false; failed = false; errorText = nil
        cookies.save()
        // Route is acknowledged by the bridge only after the page has attempted positioning.
    }

    private func navigationFailed(_ error: Error) {
        guard (error as NSError).code != NSURLErrorCancelled else { return }
        loading = false; failed = true; routeInFlight = nil
        errorText = online ? "页面加载失败，请重试。" : "网络已断开，连接恢复后会重新载入页面。"
    }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { navigationFailed(error) }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { navigationFailed(error) }
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        failed = true; routeInFlight = nil; recoverIfNeeded()
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url else { decisionHandler(.cancel); return }
        if navigationAction.targetFrame?.isMainFrame == false { decisionHandler(.allow); return }
        if AppConfiguration.isTrusted(url) {
            if navigationAction.request.httpMethod == "GET" { lastGET = url }
            decisionHandler(.allow)
        } else {
            decisionHandler(.cancel)
            if navigationAction.navigationType == .linkActivated && ["https", "http", "mailto", "tel"].contains(url.scheme ?? "") {
                UIApplication.shared.open(url)
            }
        }
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        guard let url = navigationAction.request.url else { return nil }
        if AppConfiguration.isTrusted(url) { webView.load(navigationAction.request) }
        else if ["https", "http"].contains(url.scheme ?? "") { UIApplication.shared.open(url) }
        return nil
    }

    private var presenter: UIViewController? {
        var controller = webView.window?.rootViewController
        while let presented = controller?.presentedViewController { controller = presented }
        return controller
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        guard let presenter else { completionHandler(); return }
        let alert = UIAlertController(title: "MyApp Beta", message: message, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "好", style: .default) { _ in completionHandler() })
        presenter.present(alert, animated: true)
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        guard let presenter else { completionHandler(false); return }
        let alert = UIAlertController(title: "MyApp Beta", message: message, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "取消", style: .cancel) { _ in completionHandler(false) })
        alert.addAction(UIAlertAction(title: "确定", style: .default) { _ in completionHandler(true) })
        presenter.present(alert, animated: true)
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        let origin = message.frameInfo.securityOrigin
        guard message.frameInfo.isMainFrame, origin.protocol == "https", origin.host == "xanelove.com",
              [0, 443].contains(origin.port), AppConfiguration.isTrusted(webView.url),
              let body = message.body as? [String: Any], let id = body["id"] as? String, id.count <= 100,
              let method = body["method"] as? String else { return }
        let payload = body["payload"] as? [String: Any] ?? [:]
        Task {
            do { respond(id, value: try await handle(method, payload: payload)) }
            catch { respond(id, error: error.localizedDescription) }
        }
    }

    private func handle(_ method: String, payload: [String: Any]) async throws -> Any {
        switch method {
        case "ping": return ["version": 1, "name": "MyApp Beta", "backgroundResults": AppConfiguration.backgroundResultsEnabled]
        case "media.pick":
            guard UIApplication.shared.applicationState == .active, let presenter else { throw BetaError.message("请在 App 前台选择文件。") }
            return try await media.pick(from: presenter, multiple: payload["multiple"] as? Bool ?? false,
                                        allowDocuments: payload["documents"] as? Bool ?? false,
                                        source: payload["source"] as? String ?? "choose")
        case "notifications.request": return ["granted": try await NotificationService.shared.requestPermission()]
        case "notifications.schedule":
            guard let route = ChatRoute(userInfo: payload) else { throw BetaError.message("通知需要有效的会话与消息 ID。") }
            try await NotificationService.shared.schedule(route: route, body: payload["body"] as? String ?? "有一条新消息，点击查看。",
                                                           delay: (payload["delay"] as? Double) ?? 1)
            return ["scheduled": true]
        case "reply.completed":
            guard let route = ChatRoute(userInfo: payload) else { throw BetaError.message("无效消息 ID。") }
            if UIApplication.shared.applicationState != .active {
                try await NotificationService.shared.schedule(route: route)
            }
            return ["received": true]
        case "route.handled":
            if let route = routeInFlight,
               String(describing: payload["message_id"] ?? "") == route.messageID,
               String(describing: payload["conversation_id"] ?? "") == route.conversationID {
                NotificationService.shared.acknowledge(route); routeInFlight = nil
            }
            return ["received": true]
        case "background.wait":
            guard AppConfiguration.backgroundResultsEnabled else { throw BetaError.message("后台结果接口尚未接入生产；当前 Beta 未启用此能力。") }
            try BackgroundReplySession.shared.enqueue(payload)
            return ["queued": true]
        default: throw BetaError.message("不支持的 Bridge 方法。")
        }
    }

    private func respond(_ id: String, value: Any = NSNull(), error: String? = nil) {
        guard AppConfiguration.isTrusted(webView.url) else { return }
        webView.callAsyncJavaScript("window.MyAppNative?._resolve(id, value, error)",
                                   arguments: ["id": id, "value": value, "error": error as Any? ?? NSNull()],
                                   in: nil, in: .page) { _ in }
    }
}

private final class WeakScriptHandler: NSObject, WKScriptMessageHandler {
    weak var target: WKScriptMessageHandler?
    init(_ target: WKScriptMessageHandler) { self.target = target }
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(userContentController, didReceive: message)
    }
}
