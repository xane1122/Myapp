import UIKit
import PhotosUI
import UniformTypeIdentifiers
import AVFoundation
import ImageIO

enum ImageNormalizer {
    static func jpeg(_ image: UIImage) throws -> Data {
        guard image.size.width > 0, image.size.height > 0 else { throw BetaError.message("无法解码图片。") }
        let scale = min(1, 2048 / max(image.size.width, image.size.height))
        let size = CGSize(width: max(1, (image.size.width * scale).rounded()), height: max(1, (image.size.height * scale).rounded()))
        let format = UIGraphicsImageRendererFormat(); format.scale = 1; format.opaque = true
        // UIImage.draw respects all eight EXIF orientations. Re-encoding bakes the
        // transform into pixels. Never blindly flip front-camera or library images.
        return UIGraphicsImageRenderer(size: size, format: format).jpegData(withCompressionQuality: 0.9) { context in
            UIColor.white.setFill(); context.fill(CGRect(origin: .zero, size: size))
            image.draw(in: CGRect(origin: .zero, size: size))
        }
    }

    static func file(_ url: URL, suggestedName: String? = nil) throws -> [String: Any] {
        let bytes = (try url.resourceValues(forKeys: [.fileSizeKey])).fileSize ?? 0
        guard bytes <= 40 * 1024 * 1024 else { throw BetaError.message("文件超过 40 MB，请先缩小后再选。") }
        let ext = url.pathExtension.lowercased()
        let type = UTType(filenameExtension: ext)
        if type?.conforms(to: .image) == true, ext != "gif", let source = CGImageSourceCreateWithURL(url as CFURL, nil) {
            let options: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true,
                                           kCGImageSourceCreateThumbnailWithTransform: true,
                                           kCGImageSourceThumbnailMaxPixelSize: 2048,
                                           kCGImageSourceShouldCacheImmediately: true]
            guard let cgImage = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
                throw BetaError.message("无法解码照片。")
            }
            let data = try jpeg(UIImage(cgImage: cgImage))
            return item(data, name: "photo-\(UUID().uuidString).jpg", mime: "image/jpeg")
        }
        let data = try Data(contentsOf: url)
        guard data.count <= 20 * 1024 * 1024 else { throw BetaError.message("文档或 GIF 超过 20 MB。") }
        return item(data, name: suggestedName ?? url.lastPathComponent,
                    mime: UTType(filenameExtension: ext)?.preferredMIMEType ?? "application/octet-stream")
    }

    static func item(_ data: Data, name: String, mime: String) -> [String: Any] {
        ["name": name, "type": mime, "base64": data.base64EncodedString()]
    }
}

@MainActor
final class MediaPicker: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate,
                          PHPickerViewControllerDelegate, UIDocumentPickerDelegate, UIAdaptivePresentationControllerDelegate {
    private var continuation: CheckedContinuation<[[String: Any]], Error>?
    private var multiple = false

    func pick(from presenter: UIViewController, multiple: Bool, allowDocuments: Bool, source: String) async throws -> [[String: Any]] {
        guard continuation == nil, !presenter.isBeingDismissed else { throw BetaError.message("已有选择器正在打开。") }
        self.multiple = multiple
        return try await withCheckedThrowingContinuation { continuation in
            self.continuation = continuation
            if source == "camera" { self.camera(from: presenter); return }
            if source == "library" { self.library(from: presenter); return }
            let sheet = UIAlertController(title: "添加照片或文件", message: nil, preferredStyle: .actionSheet)
            sheet.addAction(UIAlertAction(title: "拍照", style: .default) { _ in self.camera(from: presenter) })
            sheet.addAction(UIAlertAction(title: "从相册选择", style: .default) { _ in self.library(from: presenter) })
            if allowDocuments {
                sheet.addAction(UIAlertAction(title: "从文件选择", style: .default) { _ in self.documents(from: presenter) })
            }
            sheet.addAction(UIAlertAction(title: "取消", style: .cancel) { _ in self.finish(.success([])) })
            if let popover = sheet.popoverPresentationController {
                popover.sourceView = presenter.view
                popover.sourceRect = CGRect(x: presenter.view.bounds.midX, y: presenter.view.bounds.midY, width: 1, height: 1)
                popover.permittedArrowDirections = []
            }
            presenter.present(sheet, animated: true)
        }
    }

    private func camera(from presenter: UIViewController) {
        guard UIImagePickerController.isSourceTypeAvailable(.camera) else {
            finish(.failure(BetaError.message("此设备没有可用相机。"))); return
        }
        Task {
            let authorized: Bool
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized: authorized = true
            case .notDetermined: authorized = await AVCaptureDevice.requestAccess(for: .video)
            default: authorized = false
            }
            guard authorized else { finish(.failure(BetaError.message("相机权限未开启，请在 iPhone 设置中允许 MyApp Beta 使用相机。"))); return }
            let picker = UIImagePickerController()
            picker.sourceType = .camera; picker.mediaTypes = [UTType.image.identifier]
            picker.delegate = self; picker.allowsEditing = false
            picker.modalPresentationStyle = .fullScreen
            presenter.present(picker, animated: true)
        }
    }

    private func library(from presenter: UIViewController) {
        var config = PHPickerConfiguration(photoLibrary: .shared())
        config.filter = .images; config.selectionLimit = multiple ? 5 : 1
        config.preferredAssetRepresentationMode = .current
        let picker = PHPickerViewController(configuration: config)
        picker.delegate = self
        presenter.present(picker, animated: true)
        picker.presentationController?.delegate = self
    }

    private func documents(from presenter: UIViewController) {
        let picker = UIDocumentPickerViewController(forOpeningContentTypes: [.item], asCopy: true)
        picker.allowsMultipleSelection = multiple; picker.delegate = self
        presenter.present(picker, animated: true)
        picker.presentationController?.delegate = self
    }

    func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
        picker.dismiss(animated: true); finish(.success([]))
    }

    func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
        picker.dismiss(animated: true)
        do {
            guard let image = info[.originalImage] as? UIImage else { throw BetaError.message("相机没有返回照片。") }
            finish(.success([ImageNormalizer.item(try ImageNormalizer.jpeg(image), name: "camera-\(UUID().uuidString).jpg", mime: "image/jpeg")]))
        } catch { finish(.failure(error)) }
    }

    func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) {
        picker.dismiss(animated: true)
        Task {
            do {
                var items: [[String: Any]] = []
                // Sequential conversion avoids decoding multiple full-resolution photos at once.
                for result in results.prefix(multiple ? 5 : 1) {
                    let provider = result.itemProvider
                    let type = provider.registeredTypeIdentifiers.first(where: { UTType($0)?.conforms(to: .image) == true }) ?? UTType.image.identifier
                    let url: URL = try await withCheckedThrowingContinuation { callback in
                        provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                            do {
                                guard let url else { throw error ?? BetaError.message("无法读取相册文件。") }
                                let copy = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathExtension(url.pathExtension)
                                try FileManager.default.copyItem(at: url, to: copy)
                                callback.resume(returning: copy)
                            } catch { callback.resume(throwing: error) }
                        }
                    }
                    defer { try? FileManager.default.removeItem(at: url) }
                    items.append(try ImageNormalizer.file(url, suggestedName: provider.suggestedName))
                }
                finish(.success(items))
            } catch { finish(.failure(error)) }
        }
    }

    func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
        do {
            let items = try urls.prefix(multiple ? 5 : 1).map { url in
                let scoped = url.startAccessingSecurityScopedResource()
                defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                return try ImageNormalizer.file(url)
            }
            finish(.success(items))
        } catch { finish(.failure(error)) }
    }
    func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) { finish(.success([])) }
    func presentationControllerDidDismiss(_ presentationController: UIPresentationController) { finish(.success([])) }

    private func finish(_ result: Result<[[String: Any]], Error>) {
        let callback = continuation; continuation = nil
        callback?.resume(with: result)
    }
}
