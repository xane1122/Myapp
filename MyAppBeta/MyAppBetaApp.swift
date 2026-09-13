import SwiftUI
import UIKit
import UserNotifications

@main
struct MyAppBetaApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model = WebAppModel()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            ContentView(model: model)
                .onAppear {
                    if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil { model.start() }
                }
                .onOpenURL { model.openDeepLink($0) }
                .onReceive(NotificationCenter.default.publisher(for: .betaOpenChat)) { _ in model.consumePendingRoute() }
                .onChange(of: scenePhase) { phase in
                    if phase == .active {
                        model.consumePendingRoute()
                        model.recoverIfNeeded()
                        NotificationService.shared.clearBadge()
                    } else { model.persistCookies() }
                }
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = NotificationService.shared
        BackgroundReplySession.shared.reconnect()
        return true
    }

    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String,
                     completionHandler: @escaping () -> Void) {
        guard identifier == BackgroundReplySession.identifier else { completionHandler(); return }
        BackgroundReplySession.shared.setCompletionHandler(completionHandler)
    }
}

extension Notification.Name {
    static let betaOpenChat = Notification.Name("MyAppBeta.openChat")
}
