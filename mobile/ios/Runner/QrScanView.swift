import AVFoundation
import Flutter
import UIKit

enum QrCamera {
  static func authorize(_ result: @escaping FlutterResult) {
    switch AVCaptureDevice.authorizationStatus(for: .video) {
    case .authorized:
      result(true)
    case .notDetermined:
      AVCaptureDevice.requestAccess(for: .video) { granted in
        DispatchQueue.main.async { result(granted) }
      }
    default:
      result(false)
    }
  }
}

final class QrScanViewFactory: NSObject, FlutterPlatformViewFactory {
  private let messenger: FlutterBinaryMessenger

  init(messenger: FlutterBinaryMessenger) {
    self.messenger = messenger
  }

  func create(
    withFrame frame: CGRect,
    viewIdentifier viewId: Int64,
    arguments args: Any?
  ) -> FlutterPlatformView {
    QrScanPlatformView(frame: frame, viewId: viewId, messenger: messenger)
  }
}

/// 只要二维码元数据，不挂视频帧输出。像素留在 AVFoundation 里。
final class QrScanPlatformView: NSObject, FlutterPlatformView,
  AVCaptureMetadataOutputObjectsDelegate
{
  private let preview: PreviewView
  private let channel: FlutterMethodChannel
  private let session = AVCaptureSession()
  private let queue = DispatchQueue(label: "posse.qr")
  private var output: AVCaptureMetadataOutput?
  private var last: String?
  private var stopped = false

  init(frame: CGRect, viewId: Int64, messenger: FlutterBinaryMessenger) {
    preview = PreviewView(frame: frame)
    channel = FlutterMethodChannel(
      name: "posse/qr/\(viewId)",
      binaryMessenger: messenger
    )
    super.init()
    channel.setMethodCallHandler { [weak self] call, result in
      if call.method == "stop" {
        self?.stop()
        result(nil)
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
    queue.async { [weak self] in self?.configure() }
  }

  func view() -> UIView { preview }

  deinit {
    let session = session
    let output = output
    queue.async {
      output?.setMetadataObjectsDelegate(nil, queue: nil)
      if session.isRunning { session.stopRunning() }
    }
  }

  func stop() {
    queue.async { [weak self] in self?.stopOnQueue() }
  }

  private func configure() {
    if stopped { return }
    // 只要画面，不改音频会话。否则 startRunning 会先去配置 AVAudioSession。
    session.automaticallyConfiguresApplicationAudioSession = false
    guard
      let device = AVCaptureDevice.default(
        .builtInWideAngleCamera,
        for: .video,
        position: .back
      ),
      let input = try? AVCaptureDeviceInput(device: device)
    else {
      fail()
      return
    }
    // 配对码在眼前的屏幕上。近距离对焦比默认的全距离对焦更快锁住。
    tuneForScreen(device)
    session.beginConfiguration()
    if session.canSetSessionPreset(.hd1920x1080) {
      session.sessionPreset = .hd1920x1080
    } else {
      session.sessionPreset = .high
    }
    guard session.canAddInput(input) else {
      session.commitConfiguration()
      fail()
      return
    }
    session.addInput(input)
    let metadata = AVCaptureMetadataOutput()
    guard session.canAddOutput(metadata) else {
      session.commitConfiguration()
      fail()
      return
    }
    session.addOutput(metadata)
    metadata.setMetadataObjectsDelegate(self, queue: queue)
    metadata.metadataObjectTypes = [.qr]
    output = metadata
    session.commitConfiguration()
    if stopped { return }
    DispatchQueue.main.async { [weak self] in
      guard let self else { return }
      self.preview.attach(self.session)
    }
    session.startRunning()
    if stopped { session.stopRunning() }
  }

  private func tuneForScreen(_ device: AVCaptureDevice) {
    do {
      try device.lockForConfiguration()
    } catch {
      return
    }
    defer { device.unlockForConfiguration() }
    if device.isFocusPointOfInterestSupported {
      device.focusPointOfInterest = CGPoint(x: 0.5, y: 0.5)
    }
    if device.isFocusModeSupported(.continuousAutoFocus) {
      device.focusMode = .continuousAutoFocus
    }
    if device.isAutoFocusRangeRestrictionSupported {
      device.autoFocusRangeRestriction = .near
    }
    // 平滑对焦锁得慢，扫眼前的屏幕不需要。
    if device.isSmoothAutoFocusSupported {
      device.isSmoothAutoFocusEnabled = false
    }
  }

  func metadataOutput(
    _ output: AVCaptureMetadataOutput,
    didOutput metadataObjects: [AVMetadataObject],
    from connection: AVCaptureConnection
  ) {
    if stopped { return }
    for object in metadataObjects {
      guard let code = object as? AVMetadataMachineReadableCodeObject,
        let text = code.stringValue,
        !text.isEmpty,
        text != last
      else { continue }
      last = text
      DispatchQueue.main.async { [weak self] in
        self?.channel.invokeMethod("code", arguments: text)
      }
    }
  }

  private func stopOnQueue() {
    if stopped { return }
    stopped = true
    output?.setMetadataObjectsDelegate(nil, queue: nil)
    if session.isRunning { session.stopRunning() }
  }

  private func fail() {
    DispatchQueue.main.async { [weak self] in
      self?.channel.invokeMethod("failed", arguments: nil)
    }
  }
}

final class PreviewView: UIView {
  override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }

  var videoLayer: AVCaptureVideoPreviewLayer {
    layer as! AVCaptureVideoPreviewLayer
  }

  func attach(_ session: AVCaptureSession) {
    videoLayer.session = session
    videoLayer.videoGravity = .resizeAspectFill
  }
}
