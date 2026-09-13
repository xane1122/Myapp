import XCTest
import UIKit
import WebKit
@testable import MyAppBeta

final class BetaTests: XCTestCase {
    func testBundleIsolationAndOriginBoundary() {
        XCTAssertNotEqual(AppConfiguration.bundleID, AppConfiguration.legacyBundleID)
        XCTAssertFalse(AppConfiguration.backgroundResultsEnabled)
        XCTAssertTrue(AppConfiguration.isTrusted(URL(string: "https://xanelove.com/?conversation_id=1")))
        for value in ["http://xanelove.com", "https://xanelove.com.evil.test", "https://evil.test/xanelove.com", "https://xanelove.com:444", "https://user@xanelove.com", "file:///tmp/index.html"] {
            XCTAssertFalse(AppConfiguration.isTrusted(URL(string: value)), value)
        }
    }

    func testNotificationRouteValidation() throws {
        let route = try XCTUnwrap(ChatRoute(conversationID: "47", messageID: "902", assistant: "grok"))
        XCTAssertEqual(ChatRoute(userInfo: route.userInfo), route)
        let components = try XCTUnwrap(URLComponents(url: route.url, resolvingAgainstBaseURL: false))
        XCTAssertEqual(components.host, "xanelove.com")
        XCTAssertEqual(components.queryItems?.first(where: { $0.name == "message_id" })?.value, "902")
        XCTAssertEqual(components.queryItems?.first(where: { $0.name == "beta_assistant" })?.value, "grok")
        for bad in ["0", "-1", "1;alert(1)", "1.5", "٩", "9007199254740992", "9223372036854775808", ""] {
            XCTAssertNil(ChatRoute(conversationID: bad, messageID: "1"))
            XCTAssertNil(ChatRoute(conversationID: "1", messageID: bad))
        }
    }

    @MainActor
    func testAllEightImageOrientationsAreBakedIntoPixels() throws {
        let format = UIGraphicsImageRendererFormat(); format.scale = 1; format.opaque = true
        let original = UIGraphicsImageRenderer(size: CGSize(width: 48, height: 32), format: format).image { context in
            UIColor.red.setFill(); context.fill(CGRect(x: 0, y: 0, width: 24, height: 16))
            UIColor.green.setFill(); context.fill(CGRect(x: 24, y: 0, width: 24, height: 16))
            UIColor.blue.setFill(); context.fill(CGRect(x: 0, y: 16, width: 24, height: 16))
            UIColor.yellow.setFill(); context.fill(CGRect(x: 24, y: 16, width: 24, height: 16))
        }
        let cg = try XCTUnwrap(original.cgImage)
        let cases: [UIImage.Orientation] = [.up, .upMirrored, .down, .downMirrored,
                                             .left, .right, .leftMirrored, .rightMirrored]
        for orientation in cases {
            let image = UIImage(cgImage: cg, scale: 1, orientation: orientation)
            let expected = UIGraphicsImageRenderer(size: image.size, format: format).image { _ in
                image.draw(in: CGRect(origin: .zero, size: image.size))
            }
            let decoded = try XCTUnwrap(UIImage(data: ImageNormalizer.jpeg(image)))
            XCTAssertEqual(decoded.imageOrientation, .up)
            let actual = try cornerColors(decoded)
            XCTAssertEqual(actual, try cornerColors(expected), "orientation: \(orientation.rawValue)")
        }
    }

    @MainActor
    private func cornerColors(_ image: UIImage) throws -> [Int] {
        let cg = try XCTUnwrap(image.cgImage)
        let w = cg.width, h = cg.height
        var bytes = [UInt8](repeating: 0, count: w * h * 4)
        try bytes.withUnsafeMutableBytes { storage in
            let context = try XCTUnwrap(CGContext(data: storage.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
            context.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
        }
        let points = [(w/4, h/4), (3*w/4, h/4), (w/4, 3*h/4), (3*w/4, 3*h/4)]
        return points.map { x, y in
            let offset = (y * w + x) * 4
            let r = Int(bytes[offset]), g = Int(bytes[offset + 1]), b = Int(bytes[offset + 2])
            if r > 170 && g > 170 { return 3 }
            if r > g && r > b { return 0 }
            if g > r && g > b { return 1 }
            return 2
        }
    }

    @MainActor
    func testNormalizationLimitsPhotoSize() throws {
        let format = UIGraphicsImageRendererFormat(); format.scale = 1
        let image = UIGraphicsImageRenderer(size: CGSize(width: 3000, height: 100), format: format).image { context in
            UIColor.white.setFill(); context.fill(CGRect(x: 0, y: 0, width: 3000, height: 100))
        }
        let result = try XCTUnwrap(UIImage(data: ImageNormalizer.jpeg(image)))
        XCTAssertEqual(result.size.width, 2048)
        XCTAssertLessThanOrEqual(result.size.height, 100)
    }

    @MainActor
    func testWebViewUsesPersistentStorageAndBackGesture() {
        let model = WebAppModel()
        XCTAssertTrue(model.webView.configuration.websiteDataStore.isPersistent)
        XCTAssertTrue(model.webView.allowsBackForwardNavigationGestures)
        XCTAssertNil(model.webView.url) // Instantiation does not load production during tests.
    }
}
