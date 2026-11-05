import AVFoundation
import Flutter
import UIKit

/// 后台通道 posse/background：切到后台后让进程多活一会儿，连接和终端订阅不断。
/// 默认向系统要一段后台时间（约 30 秒）；设置里打开「后台保持连接」后播放和其他 App 混音的静音音频，
/// 进程一直不被挂起（要 Info.plist 的 audio 后台模式）。
final class BackgroundPlugin: NSObject {
  static let shared = BackgroundPlugin()

  private var channel: FlutterMethodChannel?
  private var keepAlive = false
  private var task: UIBackgroundTaskIdentifier = .invalid
  private var player: AVAudioPlayer?
  /// 应该在放静音：中断（来电、别的 App 独占音频）结束后据此接着放
  private var playing = false

  func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(name: "posse/background", binaryMessenger: messenger)
    self.channel = channel
    channel.setMethodCallHandler { [weak self] call, result in
      if call.method == "setKeepAlive" {
        self?.keepAlive = call.arguments as? Bool ?? false
        if self?.keepAlive == false { self?.stopSilence() }
        result(nil)
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
    let center = NotificationCenter.default
    center.addObserver(
      self, selector: #selector(resignActive),
      name: UIApplication.willResignActiveNotification, object: nil)
    center.addObserver(
      self, selector: #selector(becameActive),
      name: UIApplication.didBecomeActiveNotification, object: nil)
    center.addObserver(
      self, selector: #selector(enteredBackground),
      name: UIApplication.didEnterBackgroundNotification, object: nil)
    center.addObserver(
      self, selector: #selector(enteringForeground),
      name: UIApplication.willEnterForegroundNotification, object: nil)
    center.addObserver(
      self, selector: #selector(interrupted(_:)),
      name: AVAudioSession.interruptionNotification, object: nil)
    center.addObserver(
      self, selector: #selector(mediaReset),
      name: AVAudioSession.mediaServicesWereResetNotification, object: nil)
  }

  // 在 inactive 时就开始放：进了后台再激活音频会话可能被拒
  @objc private func resignActive() {
    if keepAlive { startSilence() }
  }

  @objc private func becameActive() {
    stopSilence()
  }

  @objc private func enteredBackground() {
    guard task == .invalid else { return }
    task = UIApplication.shared.beginBackgroundTask(withName: "posse.connection") { [weak self] in
      self?.endTask()
    }
  }

  @objc private func enteringForeground() {
    endTask()
  }

  private func endTask() {
    guard task != .invalid else { return }
    UIApplication.shared.endBackgroundTask(task)
    task = .invalid
  }

  private func startSilence() {
    playing = true
    do {
      let session = AVAudioSession.sharedInstance()
      // 混音：不打断用户正在听的，也不占控制中心的「正在播放」
      try session.setCategory(.playback, options: [.mixWithOthers])
      try session.setActive(true)
      if player == nil {
        let p = try AVAudioPlayer(data: BackgroundPlugin.silentWav())
        p.numberOfLoops = -1
        p.volume = 0
        player = p
      }
      player?.play()
    } catch {
      NSLog("posse background: silence failed: \(error)")
    }
  }

  private func stopSilence() {
    guard playing else { return }
    playing = false
    player?.stop()
    try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
  }

  @objc private func interrupted(_ note: Notification) {
    guard playing,
      let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
      AVAudioSession.InterruptionType(rawValue: raw) == .ended
    else { return }
    startSilence()
  }

  @objc private func mediaReset() {
    player = nil
    if playing { startSilence() }
  }

  /// 一秒 8 kHz 单声道 16 位的全零 WAV。
  private static func silentWav() -> Data {
    let rate: UInt32 = 8000
    let samples = Int(rate)
    let dataSize = UInt32(samples * 2)
    var d = Data()
    func put(_ s: String) { d.append(s.data(using: .ascii)!) }
    func u32(_ v: UInt32) { withUnsafeBytes(of: v.littleEndian) { d.append(contentsOf: $0) } }
    func u16(_ v: UInt16) { withUnsafeBytes(of: v.littleEndian) { d.append(contentsOf: $0) } }
    put("RIFF"); u32(36 + dataSize); put("WAVE")
    put("fmt "); u32(16); u16(1); u16(1); u32(rate); u32(rate * 2); u16(2); u16(16)
    put("data"); u32(dataSize)
    d.append(Data(count: samples * 2))
    return d
  }
}
