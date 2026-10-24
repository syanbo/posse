import Flutter
import UIKit

/// 收包线程解出的帧交给纹理。一个刷新还没取走时只留最新的一张。
final class DeskTexture: NSObject, FlutterTexture {
  private let lock = NSLock()
  private let registry: FlutterTextureRegistry
  private var latest: CVPixelBuffer?
  private var pending = false
  private(set) var textureId: Int64 = -1

  init(registry: FlutterTextureRegistry) {
    self.registry = registry
    super.init()
    textureId = registry.register(self)
  }

  func present(_ buffer: CVPixelBuffer) {
    var notify = false
    lock.lock()
    latest = buffer
    if !pending {
      pending = true
      notify = true
    }
    lock.unlock()
    if notify {
      registry.textureFrameAvailable(textureId)
    }
  }

  func copyPixelBuffer() -> Unmanaged<CVPixelBuffer>? {
    lock.lock()
    let buffer = latest
    pending = false
    lock.unlock()
    guard let buffer else { return nil }
    return Unmanaged.passRetained(buffer)
  }

  func unregister() {
    guard textureId >= 0 else { return }
    registry.unregisterTexture(textureId)
    textureId = -1
    lock.lock()
    latest = nil
    lock.unlock()
  }
}

final class DeskPlugin: NSObject, FlutterStreamHandler {
  private let textures: FlutterTextureRegistry
  private let method: FlutterMethodChannel
  private let events: FlutterEventChannel
  private let control = DispatchQueue(label: "hive.desk.control")
  /// 主线程的暂停和 control 队列的 stop 都会碰到 session。只在这把锁里换引用。
  private let sessionLock = NSLock()
  private var texture: DeskTexture?
  private var session: DeskSession?
  private var sink: FlutterEventSink?
  private var generation = 0

  init(textures: FlutterTextureRegistry, messenger: FlutterBinaryMessenger) {
    self.textures = textures
    method = FlutterMethodChannel(name: "hive/desk", binaryMessenger: messenger)
    events = FlutterEventChannel(name: "hive/desk/stats", binaryMessenger: messenger)
    super.init()
  }

  func install() {
    method.setMethodCallHandler { [weak self] call, result in
      self?.handle(call, result: result)
    }
    events.setStreamHandler(self)
    NotificationCenter.default.addObserver(
      self,
      selector: #selector(enteredBackground),
      name: UIApplication.didEnterBackgroundNotification,
      object: nil
    )
    NotificationCenter.default.addObserver(
      self,
      selector: #selector(enteredForeground),
      name: UIApplication.willEnterForegroundNotification,
      object: nil
    )
  }

  func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    sink = events
    return nil
  }

  func onCancel(withArguments arguments: Any?) -> FlutterError? {
    sink = nil
    return nil
  }

  @objc private func enteredBackground() {
    setSessionPaused(true)
  }

  @objc private func enteredForeground() {
    setSessionPaused(false)
  }

  /// 先把引用拷出来再调，避免和 stop 置空抢同一份 session。
  private func setSessionPaused(_ value: Bool) {
    sessionLock.lock()
    let current = session
    sessionLock.unlock()
    current?.setPaused(value)
  }

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "start":
      start(call.arguments, result: result)
    case "stop":
      stop(result: result)
    case "pause":
      setSessionPaused(true)
      result(nil)
    case "resume":
      setSessionPaused(false)
      result(nil)
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  private func start(_ arguments: Any?, result: @escaping FlutterResult) {
    guard
      let args = arguments as? [String: Any],
      let host = args["host"] as? String,
      let port = (args["port"] as? NSNumber)?.intValue ?? args["port"] as? Int,
      let secret = bytes(args["secret"]),
      let nonce = bytes(args["nonce"]),
      let codec = DeskCodec(rawValue: args["codec"] as? String ?? "")
    else {
      result(FlutterError(code: "bad_args", message: "主机、端口或密钥不对", details: nil))
      return
    }
    guard port > 0, port < 65536, let peer = deskResolve(host: host, port: UInt16(port)) else {
      result(FlutterError(code: "bad_address", message: "解析不了 \(host)", details: nil))
      return
    }
    control.async { [weak self] in
      guard let self else { return }
      let generation = self.stopSession()
      DispatchQueue.main.async {
        self.sessionLock.lock()
        let live = self.generation == generation
        self.sessionLock.unlock()
        guard live else {
          result(FlutterError(code: "cancelled", message: "连接已取消", details: nil))
          return
        }
        do {
          let session = try DeskSession(secret: secret, nonce: nonce, peer: peer, codec: codec)
          let texture = DeskTexture(registry: self.textures)
          session.onFrame = { [weak texture] image in
            texture?.present(image)
          }
          session.onStats = { [weak self] stats in
            DispatchQueue.main.async {
              self?.sink?(stats)
            }
          }
          self.sessionLock.lock()
          guard self.generation == generation else {
            self.sessionLock.unlock()
            texture.unregister()
            result(FlutterError(code: "cancelled", message: "连接已取消", details: nil))
            return
          }
          self.session = session
          self.sessionLock.unlock()
          session.start()
          if UIApplication.shared.applicationState == .background {
            session.setPaused(true)
          }
          self.texture = texture
          result(texture.textureId)
        } catch DeskError.unsupportedCodec {
          result(FlutterError(code: "unsupported_codec", message: "这一步只解 HEVC", details: nil))
        } catch {
          result(FlutterError(code: "desk", message: "打不开接收", details: nil))
        }
      }
    }
  }

  private func stop(result: @escaping FlutterResult) {
    control.async { [weak self] in
      guard let self else {
        DispatchQueue.main.async { result(nil) }
        return
      }
      self.stopSession()
      DispatchQueue.main.async {
        self.texture?.unregister()
        self.texture = nil
        result(nil)
      }
    }
  }

  /// 在 control 队列上停掉收包线程。先摘掉引用再等线程，锁不盖住等待。
  /// 返回的代号用来认出这次停掉之后才开始的连接。
  @discardableResult
  private func stopSession() -> Int {
    sessionLock.lock()
    generation += 1
    let token = generation
    let current = session
    session = nil
    sessionLock.unlock()
    if let current {
      current.onFrame = nil
      current.onStats = nil
      current.requestStop()
      current.waitUntilStopped()
    }
    return token
  }

  private func bytes(_ value: Any?) -> Data? {
    if let typed = value as? FlutterStandardTypedData { return typed.data }
    if let data = value as? Data { return data }
    return nil
  }
}
