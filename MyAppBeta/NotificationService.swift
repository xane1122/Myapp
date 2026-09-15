import Foundation
import UserNotifications
import UIKit

final class NotificationService: NSObject, UNUserNotificationCenterDelegate {
    static let shared = NotificationService()
    static let previewPreferenceKey = "beta.notificationShowsPreview"
    private let center = UNUserNotificationCenter.current()
    private let pendingKey = "beta.pendingChatRoute"

    func requestPermission() async throws -> Bool {
        try await center.requestAuthorization(options: [.alert, .sound, .badge])
    }

    func schedule(route: ChatRoute?, title: String = "Rhys", body: String = "有一条新回复，点击查看。",
                  delay: TimeInterval = 1, identifier: String? = nil) async throws {
        let settings = await center.notificationSettings()
        guard [.authorized, .provisional, .ephemeral].contains(settings.authorizationStatus) else {
            throw BetaError.message("请先在 Beta 设置中允许通知；也可前往 iPhone 设置开启。")
        }
        let requests = await center.pendingNotificationRequests()
        let id = identifier ?? route.map { "reply.\($0.conversationID).\($0.messageID)" } ?? "beta.test"
        guard requests.count < 50 || requests.contains(where: { $0.identifier == id }) else {
            throw BetaError.message("本地通知计划已达 50 条，请取消部分计划后再试。")
        }
        let content = UNMutableNotificationContent()
        content.title = String(title.prefix(80))
        let showsPreview = UserDefaults.standard.object(forKey: Self.previewPreferenceKey) as? Bool ?? true
        content.body = String((showsPreview ? body : "有一条新消息，点击查看。").prefix(240))
        content.sound = .default
        content.badge = 1
        content.userInfo = route?.userInfo ?? [:]
        content.threadIdentifier = route.map { "chat.\($0.conversationID)" } ?? "beta.test"
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: min(max(delay, 1), 604800), repeats: false)
        try await center.add(UNNotificationRequest(identifier: id, content: content, trigger: trigger))
    }

    func store(_ route: ChatRoute) {
        UserDefaults.standard.set(try? JSONEncoder().encode(route), forKey: pendingKey)
        DispatchQueue.main.async { NotificationCenter.default.post(name: .betaOpenChat, object: nil) }
    }

    func pendingRoute() -> ChatRoute? {
        guard let data = UserDefaults.standard.data(forKey: pendingKey),
              let value = try? JSONDecoder().decode(ChatRoute.self, from: data) else { return nil }
        return ChatRoute(conversationID: value.conversationID, messageID: value.messageID, assistant: value.assistant)
    }

    func acknowledge(_ route: ChatRoute) {
        if pendingRoute() == route { UserDefaults.standard.removeObject(forKey: pendingKey) }
    }

    func clearBadge() { center.setBadgeCount(0) { _ in } }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound, .badge])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        if let route = ChatRoute(userInfo: response.notification.request.content.userInfo) { store(route) }
        completionHandler()
    }
}
