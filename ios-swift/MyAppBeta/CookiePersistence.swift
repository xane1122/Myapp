import Foundation
import WebKit

// WKWebsiteDataStore.default owns localStorage/IndexedDB/cache. This supplements it
// with session-cookie restoration after process termination, within Beta's sandbox.
@MainActor
final class CookiePersistence: NSObject, WKHTTPCookieStoreObserver {
    private let store: WKHTTPCookieStore
    private let file: URL

    init(store: WKHTTPCookieStore) {
        self.store = store
        let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        file = directory.appendingPathComponent("beta-cookies.plist")
        super.init()
    }

    private func belongsToApp(_ cookie: HTTPCookie) -> Bool {
        let domain = cookie.domain.hasPrefix(".") ? String(cookie.domain.dropFirst()) : cookie.domain
        return domain.lowercased() == "xanelove.com"
    }

    func restore() async {
        let existing = await store.allCookies()
        if let data = try? Data(contentsOf: file),
           let rows = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [[String: Any]] {
            for row in rows {
                let properties = Dictionary(uniqueKeysWithValues: row.map { (HTTPCookiePropertyKey($0.key), $0.value) })
                guard let cookie = HTTPCookie(properties: properties), belongsToApp(cookie),
                      cookie.expiresDate.map({ $0 > Date() }) ?? true,
                      !existing.contains(where: { $0.name == cookie.name && $0.domain == cookie.domain && $0.path == cookie.path }) else { continue }
                await store.setCookie(cookie)
            }
        }
        store.add(self)
    }

    func cookiesDidChange(in cookieStore: WKHTTPCookieStore) { save() }

    func save() {
        store.getAllCookies { [weak self] cookies in
            guard let self else { return }
            let rows = cookies.filter(self.belongsToApp).compactMap { cookie -> [String: Any]? in
                guard let properties = cookie.properties else { return nil }
                return Dictionary(uniqueKeysWithValues: properties.map { ($0.key.rawValue, $0.value) })
            }
            guard let data = try? PropertyListSerialization.data(fromPropertyList: rows, format: .binary, options: 0) else { return }
            try? data.write(to: self.file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            var resource = self.file
            var values = URLResourceValues(); values.isExcludedFromBackup = true
            try? resource.setResourceValues(values)
        }
    }
}
