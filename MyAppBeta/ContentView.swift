import SwiftUI
import WebKit

struct ContentView: View {
    @ObservedObject var model: WebAppModel

    var body: some View {
        ZStack(alignment: .top) {
            // The website already uses viewport-fit=cover and CSS safe-area insets.
            // Extend its background to the screen edges while retaining keyboard avoidance.
            PersistentWebView(webView: model.webView)
                .ignoresSafeArea(.container)
            if !model.online {
                Text("当前离线 · 联网后自动恢复加载").font(.caption).frame(maxWidth: .infinity).padding(6).background(Color.orange.opacity(0.14))
            }
            if model.loading { ProgressView().padding(10).background(.regularMaterial, in: Capsule()).padding(.top, 8) }
            if let error = model.errorText {
                VStack(spacing: 16) {
                    Image(systemName: "wifi.exclamationmark").font(.largeTitle)
                    Text(error).multilineTextAlignment(.center)
                    Button("重新连接") { model.recoverIfNeeded() }.buttonStyle(.borderedProminent).disabled(!model.online)
                }
                .padding(28).frame(maxWidth: .infinity, maxHeight: .infinity).background(Color(uiColor: .systemBackground))
            }
        }
        .tint(Color(red: 0.65, green: 0.38, blue: 0.48))
        .sheet(isPresented: $model.showSettings) { BetaSettingsView(model: model) }
    }
}

private struct PersistentWebView: UIViewRepresentable {
    let webView: WKWebView
    func makeUIView(context: Context) -> WKWebView { webView }
    func updateUIView(_ uiView: WKWebView, context: Context) {}
}

private struct BetaSettingsView: View {
    @ObservedObject var model: WebAppModel
    @Environment(\.dismiss) private var dismiss
    @State private var result = ""

    var body: some View {
        NavigationStack {
            Form {
                Section("MyApp Beta") {
                    Text("独立安装，连接现有 MyApp 网站。聊天数据与正式版共用，设备上的网页存储独立保存。")
                    Button("重新加载网页") { model.reload(); dismiss() }
                }
                Section("本地通知") {
                    Button("开启通知、声音和角标") {
                        Task {
                            do { result = try await NotificationService.shared.requestPermission() ? "通知已允许。" : "通知未允许，可前往 iPhone 设置开启。" }
                            catch { result = error.localizedDescription }
                        }
                    }
                    Button("5 秒后发送测试通知") {
                        Task {
                            do {
                                try await NotificationService.shared.schedule(route: nil, body: "这是 MyApp Beta 的本地通知。", delay: 5)
                                result = "已预约；可切到后台测试声音与角标。"
                            } catch { result = error.localizedDescription }
                        }
                    }
                    Text("已有回复可携带会话和消息定位。本地通知不是远程推送；后台结果下载尚未接入服务器。")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if !result.isEmpty { Section { Text(result).accessibilityIdentifier("beta.status") } }
                Section("隐私与权限") {
                    Text("相机只在拍照时请求；相册使用系统选择器，仅读取选中的照片。不会导入正式版 Cookie，也不会添加网页登录密码。")
                    Button("打开系统设置") {
                        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                    }
                }
            }
            .navigationTitle("Beta 设置")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("完成") { dismiss() } } }
        }
    }
}
