import AVFoundation
import Flutter
import Security
import Speech
import UIKit

/// 手机端语音录音与系统离线识别通道（posse/voice + posse/voice/events）。
/// 所有控制状态与回调均在主线程串行执行；录音/识别期间向 BackgroundPlugin 声明音频所有权。
final class VoicePlugin: NSObject, FlutterStreamHandler {
  static let shared = VoicePlugin()

  private var methodChannel: FlutterMethodChannel?
  private var eventChannel: FlutterEventChannel?
  private var eventSink: FlutterEventSink?

  private var activeSessionId: String?
  private var audioSessionClaimed = false

  private var recorder: AVAudioRecorder?
  private var recordingPath: String?
  private var recordingStartedAt: TimeInterval = 0
  private var meterTimer: Timer?

  private var audioEngine: AVAudioEngine?
  private var tapInstalled = false
  private var speechRecognizer: SFSpeechRecognizer?
  private var recognitionRequest: SFSpeechAudioBufferRecognitionRequest?
  private var recognitionTask: SFSpeechRecognitionTask?
  private var latestTranscript = ""
  private var nativeActive = false
  private var nativeFinishing = false
  private var lastLevelEmittedAt: TimeInterval = 0

  private var observersInstalled = false

  func install(messenger: FlutterBinaryMessenger) {
    let mc = FlutterMethodChannel(name: "posse/voice", binaryMessenger: messenger)
    mc.setMethodCallHandler { [weak self] call, result in
      self?.handle(call, result: result)
    }
    methodChannel = mc

    let ec = FlutterEventChannel(name: "posse/voice/events", binaryMessenger: messenger)
    ec.setStreamHandler(self)
    eventChannel = ec

    VoiceSecret.install(messenger: messenger)
    installObservers()
  }

  private func installObservers() {
    guard !observersInstalled else { return }
    observersInstalled = true
    let center = NotificationCenter.default
    center.addObserver(
      self, selector: #selector(onAudioInterruption(_:)),
      name: AVAudioSession.interruptionNotification, object: nil)
    center.addObserver(
      self, selector: #selector(onRouteChange(_:)),
      name: AVAudioSession.routeChangeNotification, object: nil)
    center.addObserver(
      self, selector: #selector(onEnteredBackground),
      name: UIApplication.didEnterBackgroundNotification, object: nil)
  }

  // MARK: - FlutterStreamHandler

  func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    eventSink = events
    return nil
  }

  func onCancel(withArguments arguments: Any?) -> FlutterError? {
    eventSink = nil
    return nil
  }

  private func emit(_ payload: [String: Any]) {
    if Thread.isMainThread {
      eventSink?(payload)
    } else {
      DispatchQueue.main.async { [weak self] in
        self?.eventSink?(payload)
      }
    }
  }

  // MARK: - MethodChannel

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    let args = call.arguments as? [String: Any] ?? [:]
    switch call.method {
    case "capabilities":
      let localeId = args["locale"] as? String
      let recognizer = Self.makeRecognizer(localeId: localeId)
      let available = recognizer?.isAvailable ?? false
      let onDevice = available && (recognizer?.supportsOnDeviceRecognition ?? false)
      result([
        "nativeAvailable": available,
        "onDeviceAvailable": onDevice,
        "maxNativeSeconds": 55,
      ])
    case "authorize":
      let needSpeech = args["needSpeech"] as? Bool ?? false
      authorize(needSpeech: needSpeech, result: result)
    case "openSettings":
      if let url = URL(string: UIApplication.openSettingsURLString) {
        UIApplication.shared.open(url) { ok in result(ok) }
      } else {
        result(false)
      }
    case "startRecording":
      guard let path = args["path"] as? String, !path.isEmpty else {
        result(FlutterError(code: "bad_args", message: "missing path", details: nil))
        return
      }
      let sessionId = args["sessionId"] as? String ?? UUID().uuidString
      let format = args["format"] as? String ?? "m4a"
      startRecording(sessionId: sessionId, path: path, format: format, result: result)
    case "stopRecording":
      let sessionId = args["sessionId"] as? String
      result(stopRecording(sessionId: sessionId, deleteFile: false))
    case "cancelRecording":
      let sessionId = args["sessionId"] as? String
      _ = stopRecording(sessionId: sessionId, deleteFile: true)
      result(nil)
    case "startNativeRecognition":
      let sessionId = args["sessionId"] as? String ?? UUID().uuidString
      let locale = args["locale"] as? String
      let hints = args["hints"] as? [String] ?? []
      startNativeRecognition(sessionId: sessionId, localeId: locale, hints: hints, result: result)
    case "stopNativeRecognition":
      let sessionId = args["sessionId"] as? String
      result(stopNativeRecognition(sessionId: sessionId, cancel: false))
    case "cancelNativeRecognition":
      let sessionId = args["sessionId"] as? String
      _ = stopNativeRecognition(sessionId: sessionId, cancel: true)
      result(nil)
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  private static func makeRecognizer(localeId: String?) -> SFSpeechRecognizer? {
    if let localeId, !localeId.isEmpty {
      if let r = SFSpeechRecognizer(locale: Locale(identifier: localeId)) {
        return r
      }
    }
    if let current = SFSpeechRecognizer() {
      return current
    }
    let preferZh = Locale.preferredLanguages.first?.lowercased().hasPrefix("zh") == true
    let fallbacks = preferZh ? ["zh-CN", "en-US"] : ["en-US", "zh-CN"]
    for id in fallbacks {
      if let r = SFSpeechRecognizer(locale: Locale(identifier: id)) {
        return r
      }
    }
    return nil
  }

  // MARK: - Authorization

  private func authorize(needSpeech: Bool, result: @escaping FlutterResult) {
    let finishSpeech: (String) -> Void = { micState in
      guard micState == "granted", needSpeech else {
        result([
          "mic": micState,
          "speech": needSpeech ? "denied" : "not_needed",
        ])
        return
      }
      let current = SFSpeechRecognizer.authorizationStatus()
      if current != .notDetermined {
        result([
          "mic": micState,
          "speech": Self.speechStatusName(current),
        ])
        return
      }
      SFSpeechRecognizer.requestAuthorization { status in
        DispatchQueue.main.async {
          result([
            "mic": micState,
            "speech": Self.speechStatusName(status),
          ])
        }
      }
    }

    let micStatus = AVAudioSession.sharedInstance().recordPermission
    switch micStatus {
    case .granted:
      finishSpeech("granted")
    case .denied:
      finishSpeech("denied")
    case .undetermined:
      AVAudioSession.sharedInstance().requestRecordPermission { granted in
        DispatchQueue.main.async {
          finishSpeech(granted ? "granted" : "denied")
        }
      }
    @unknown default:
      finishSpeech("denied")
    }
  }

  private static func speechStatusName(_ status: SFSpeechRecognizerAuthorizationStatus) -> String {
    switch status {
    case .authorized: return "granted"
    case .restricted: return "restricted"
    case .denied, .notDetermined: return "denied"
    @unknown default: return "denied"
    }
  }

  // MARK: - Audio Session

  private func configureAudioSession(forNativeSpeech: Bool) throws {
    BackgroundPlugin.shared.beginAudioCapture()
    audioSessionClaimed = true
    do {
      let session = AVAudioSession.sharedInstance()
      if forNativeSpeech {
        // Apple 官方推荐系统语音识别使用 .record + .measurement，避免 .playAndRecord 的蓝牙路由/DSP 冲突。
        try session.setCategory(
          .record,
          mode: .measurement,
          options: [.duckOthers]
        )
        try session.setActive(true, options: .notifyOthersOnDeactivation)
      } else {
        // 文件录音使用 .default 模式并暂停背景音乐防串音；结束时 notifyOthersOnDeactivation 恢复音乐。
        try session.setCategory(
          .playAndRecord,
          mode: .default,
          options: [.allowBluetooth, .defaultToSpeaker]
        )
        try session.setActive(true)
      }
    } catch {
      releaseAudioSession(restoreBackgroundIfNeeded: false)
      throw error
    }
  }

  private func releaseAudioSession(restoreBackgroundIfNeeded: Bool) {
    guard audioSessionClaimed else { return }
    audioSessionClaimed = false
    try? AVAudioSession.sharedInstance().setActive(
      false,
      options: [.notifyOthersOnDeactivation]
    )
    BackgroundPlugin.shared.endAudioCapture(resumeSilenceIfBackground: restoreBackgroundIfNeeded)
  }

  /// 新录音启动前先终止任意已有采集并通知旧会话已中断，保证进程级独占。
  private func preemptActiveCapture() {
    if recorder != nil {
      let oldSid = activeSessionId ?? ""
      if let info = stopRecording(sessionId: nil, deleteFile: false, restoreBackground: false) {
        emit([
          "type": "interrupted",
          "sessionId": oldSid,
          "path": info["path"] ?? "",
          "durationMs": info["durationMs"] ?? 0,
          "reason": "preempted",
        ])
      }
    }
    if nativeActive {
      let oldSid = activeSessionId ?? ""
      let text = latestTranscript
      cleanupNative(restoreBackground: false)
      emit([
        "type": "interrupted",
        "sessionId": oldSid,
        "text": text,
        "durationMs": 0,
        "reason": "preempted",
      ])
    }
  }

  // MARK: - File Recording

  private func startRecording(
    sessionId: String,
    path: String,
    format: String,
    result: @escaping FlutterResult
  ) {
    preemptActiveCapture()

    let url = URL(fileURLWithPath: path)
    do {
      try FileManager.default.createDirectory(
        at: url.deletingLastPathComponent(),
        withIntermediateDirectories: true
      )
      try configureAudioSession(forNativeSpeech: false)

      let settings: [String: Any]
      if format == "wav" {
        settings = [
          AVFormatIDKey: Int(kAudioFormatLinearPCM),
          AVSampleRateKey: 16000.0,
          AVNumberOfChannelsKey: 1,
          AVLinearPCMBitDepthKey: 16,
          AVLinearPCMIsFloatKey: false,
          AVLinearPCMIsBigEndianKey: false,
        ]
      } else {
        settings = [
          AVFormatIDKey: Int(kAudioFormatMPEG4AAC),
          AVSampleRateKey: 16000.0,
          AVNumberOfChannelsKey: 1,
          AVEncoderBitRateKey: 32000,
          AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
        ]
      }

      let rec = try AVAudioRecorder(url: url, settings: settings)
      rec.isMeteringEnabled = true
      guard rec.prepareToRecord(), rec.record() else {
        try? FileManager.default.removeItem(atPath: path)
        releaseAudioSession(restoreBackgroundIfNeeded: false)
        result(FlutterError(code: "record_failed", message: "failed to start audio recorder", details: nil))
        return
      }
      activeSessionId = sessionId
      recorder = rec
      recordingPath = path
      recordingStartedAt = Date().timeIntervalSince1970
      startMeterTimer(sessionId: sessionId)
      result(true)
    } catch {
      try? FileManager.default.removeItem(atPath: path)
      releaseAudioSession(restoreBackgroundIfNeeded: false)
      result(FlutterError(code: "record_failed", message: error.localizedDescription, details: nil))
    }
  }

  private func stopRecording(
    sessionId: String?,
    deleteFile: Bool,
    restoreBackground: Bool = false
  ) -> [String: Any]? {
    if let sessionId, !sessionId.isEmpty, activeSessionId != sessionId {
      return nil
    }
    meterTimer?.invalidate()
    meterTimer = nil

    guard let rec = recorder else { return nil }
    let path = recordingPath
    let durationMs = max(0, Int((Date().timeIntervalSince1970 - recordingStartedAt) * 1000))
    rec.stop()
    recorder = nil
    recordingPath = nil
    recordingStartedAt = 0
    activeSessionId = nil
    releaseAudioSession(restoreBackgroundIfNeeded: restoreBackground)

    if deleteFile, let path {
      try? FileManager.default.removeItem(atPath: path)
      return nil
    }
    guard let path,
      let attrs = try? FileManager.default.attributesOfItem(atPath: path),
      let size = attrs[.size] as? NSNumber,
      size.intValue > 0
    else {
      if let path { try? FileManager.default.removeItem(atPath: path) }
      return nil
    }
    return [
      "path": path,
      "durationMs": durationMs,
    ]
  }

  private func startMeterTimer(sessionId: String) {
    meterTimer?.invalidate()
    let timer = Timer(timeInterval: 0.06, repeats: true) { [weak self] _ in
      guard let self, self.activeSessionId == sessionId, let rec = self.recorder, rec.isRecording else { return }
      rec.updateMeters()
      let db = rec.averagePower(forChannel: 0)
      self.emit([
        "type": "level",
        "sessionId": sessionId,
        "level": Self.normalizeDb(db),
      ])
    }
    RunLoop.main.add(timer, forMode: .common)
    meterTimer = timer
  }

  private static func normalizeDb(_ db: Float) -> Double {
    if !db.isFinite || db <= -55 { return 0.0 }
    if db >= -5 { return 1.0 }
    return Double((db + 55) / 50)
  }

  // MARK: - Native Speech Recognition

  private func startNativeRecognition(
    sessionId: String,
    localeId: String?,
    hints: [String],
    result: @escaping FlutterResult
  ) {
    preemptActiveCapture()

    guard let recognizer = Self.makeRecognizer(localeId: localeId),
      recognizer.isAvailable
    else {
      result(FlutterError(code: "speech_unavailable", message: "speech recognizer unavailable", details: nil))
      return
    }

    do {
      try configureAudioSession(forNativeSpeech: true)
      let engine = AVAudioEngine()
      let req = SFSpeechAudioBufferRecognitionRequest()
      req.shouldReportPartialResults = true
      // 不强锁 requiresOnDeviceRecognition：离线语音包就绪时 iOS 仍自动离线识别，
      // 未下载离线包或关闭键盘听写时可平滑回退到 Apple 系统语音服务，避免真机报 1101。
      req.requiresOnDeviceRecognition = false
      req.taskHint = .dictation
      if #available(iOS 16.0, *) {
        req.addsPunctuation = true
      }
      if !hints.isEmpty {
        req.contextualStrings = Array(hints.prefix(100))
      }

      let input = engine.inputNode
      let format = input.outputFormat(forBus: 0)
      guard format.sampleRate > 0 && format.channelCount > 0 else {
        releaseAudioSession(restoreBackgroundIfNeeded: false)
        result(FlutterError(code: "speech_failed", message: "invalid audio input format", details: nil))
        return
      }
      lastLevelEmittedAt = 0
      input.installTap(onBus: 0, bufferSize: 1024, format: format) { [weak self] buffer, _ in
        req.append(buffer)
        guard let self, let ch = buffer.floatChannelData?[0] else { return }
        let now = CACurrentMediaTime()
        guard now - self.lastLevelEmittedAt >= 0.06 else { return }
        self.lastLevelEmittedAt = now
        let frames = Int(buffer.frameLength)
        guard frames > 0 else { return }
        var sum: Float = 0
        for i in 0..<frames {
          let s = ch[i]
          sum += s * s
        }
        let rms = sqrt(sum / Float(frames))
        let db = rms > 0 ? 20 * log10(rms) : -60
        self.emit([
          "type": "level",
          "sessionId": sessionId,
          "level": Self.normalizeDb(db),
        ])
      }
      tapInstalled = true
      audioEngine = engine
      recognitionRequest = req
      speechRecognizer = recognizer
      activeSessionId = sessionId
      latestTranscript = ""
      nativeActive = true
      nativeFinishing = false

      // 先建 recognitionTask 再启动音频引擎，避免首个音频帧先于任务挂载被丢弃或触发状态异常。
      var taskRef: SFSpeechRecognitionTask?
      taskRef = recognizer.recognitionTask(with: req) { [weak self] resp, error in
        DispatchQueue.main.async {
          guard let self,
            self.activeSessionId == sessionId,
            self.recognitionTask === taskRef
          else { return }
          if let resp {
            let text = resp.bestTranscription.formattedString
            self.latestTranscript = text
            if resp.isFinal {
              self.cleanupNative(restoreBackground: false, cancelTask: false)
              self.emit([
                "type": "final",
                "sessionId": sessionId,
                "text": text,
              ])
              return
            } else {
              self.emit([
                "type": "partial",
                "sessionId": sessionId,
                "text": text,
              ])
            }
          }
          if let error {
            let nsErr = error as NSError
            NSLog("posse voice: native speech error domain=\(nsErr.domain) code=\(nsErr.code) msg=\(nsErr.localizedDescription)")
            let active = self.nativeActive
            let finishing = self.nativeFinishing
            let fallback = self.latestTranscript
            self.cleanupNative(restoreBackground: false, cancelTask: true)
            if finishing && !fallback.isEmpty {
              self.emit([
                "type": "final",
                "sessionId": sessionId,
                "text": fallback,
              ])
            } else if active {
              let isNoSpeech = nsErr.domain == "kAFAssistantErrorDomain"
                && (nsErr.code == 1110 || nsErr.code == 203)
              self.emit([
                "type": "error",
                "sessionId": sessionId,
                "code": isNoSpeech ? "empty_result" : "speech_failed",
                "message": "[\(nsErr.domain):\(nsErr.code)] \(nsErr.localizedDescription)",
              ])
            }
          }
        }
      }
      recognitionTask = taskRef

      engine.prepare()
      try engine.start()
      result(true)
    } catch {
      NSLog("posse voice: startNativeRecognition failed: \(error)")
      cleanupNative(restoreBackground: false, cancelTask: true)
      result(FlutterError(code: "speech_failed", message: error.localizedDescription, details: nil))
    }
  }

  private func stopNativeRecognition(
    sessionId: String?,
    cancel: Bool,
    restoreBackground: Bool = false
  ) -> String? {
    if let sessionId, !sessionId.isEmpty, activeSessionId != sessionId {
      return nil
    }
    guard nativeActive else { return nil }
    let sid = activeSessionId ?? ""
    let current = latestTranscript
    if cancel {
      recognitionTask?.cancel()
      cleanupNative(restoreBackground: restoreBackground)
      return nil
    }
    nativeFinishing = true
    stopEngineAndTap()
    recognitionRequest?.endAudio()
    recognitionTask?.finish()
    let task = recognitionTask
    DispatchQueue.main.asyncAfter(deadline: .now() + 2.0) { [weak self] in
      guard let self,
        self.activeSessionId == sid,
        self.recognitionTask === task,
        self.nativeActive
      else { return }
      let text = self.latestTranscript
      self.cleanupNative(restoreBackground: restoreBackground, cancelTask: true)
      self.emit([
        "type": "final",
        "sessionId": sid,
        "text": text,
      ])
    }
    return current
  }

  private func stopEngineAndTap() {
    if tapInstalled {
      audioEngine?.inputNode.removeTap(onBus: 0)
      tapInstalled = false
    }
    audioEngine?.stop()
  }

  private func cleanupNative(restoreBackground: Bool, cancelTask: Bool = true) {
    stopEngineAndTap()
    nativeActive = false
    nativeFinishing = false
    audioEngine = nil
    recognitionRequest?.endAudio()
    recognitionRequest = nil
    let task = recognitionTask
    recognitionTask = nil
    if cancelTask {
      task?.cancel()
    }
    speechRecognizer = nil
    if recorder == nil {
      activeSessionId = nil
    }
    releaseAudioSession(restoreBackgroundIfNeeded: restoreBackground)
  }

  // MARK: - Interruptions & Background

  @objc private func onAudioInterruption(_ note: Notification) {
    let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt
    DispatchQueue.main.async { [weak self] in
      guard let self, let raw, AVAudioSession.InterruptionType(rawValue: raw) == .began else { return }
      self.interruptActiveCapture(reason: "audio_interrupted", restoreBackground: false)
    }
  }

  @objc private func onRouteChange(_ note: Notification) {
    let raw = note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt
    DispatchQueue.main.async { [weak self] in
      guard let self, let raw, AVAudioSession.RouteChangeReason(rawValue: raw) == .oldDeviceUnavailable else { return }
      self.interruptActiveCapture(reason: "route_changed", restoreBackground: false)
    }
  }

  @objc private func onEnteredBackground() {
    DispatchQueue.main.async { [weak self] in
      self?.interruptActiveCapture(reason: "background", restoreBackground: true)
    }
  }

  private func interruptActiveCapture(reason: String, restoreBackground: Bool) {
    if recorder != nil {
      let sid = activeSessionId ?? ""
      if let info = stopRecording(sessionId: nil, deleteFile: false, restoreBackground: restoreBackground) {
        emit([
          "type": "interrupted",
          "sessionId": sid,
          "path": info["path"] ?? "",
          "durationMs": info["durationMs"] ?? 0,
          "reason": reason,
        ])
      } else {
        emit([
          "type": "error",
          "sessionId": sid,
          "code": "record_failed",
          "message": reason,
        ])
      }
      return
    }
    if nativeActive {
      let sid = activeSessionId ?? ""
      let text = latestTranscript
      let wasFinishing = nativeFinishing
      cleanupNative(restoreBackground: restoreBackground)
      if wasFinishing && !text.isEmpty {
        emit([
          "type": "final",
          "sessionId": sid,
          "text": text,
        ])
      } else {
        emit([
          "type": "interrupted",
          "sessionId": sid,
          "text": text,
          "durationMs": 0,
          "reason": reason,
        ])
      }
    }
  }
}

/// 语音 API Key 的钥匙串存储（posse/voice/secret）：仅本机解锁可读、不进备份、不随迁移。
/// 更新已有项时优先 SecItemUpdate，避免先删后加在写失败时丢掉旧 Key。
private enum VoiceSecret {
  private static let service = "com.dengbo.posse.voice-secret"

  static func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(
      name: "posse/voice/secret",
      binaryMessenger: messenger
    )
    channel.setMethodCallHandler { call, result in
      let args = call.arguments as? [String: Any] ?? [:]
      if call.method == "clearAll" {
        SecItemDelete(
          [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
          ] as CFDictionary)
        result(nil)
        return
      }
      guard let key = args["key"] as? String, !key.isEmpty else {
        result(FlutterError(code: "bad_args", message: "missing key", details: nil))
        return
      }
      switch call.method {
      case "has":
        result(has(key))
      case "read":
        result(read(key))
      case "save":
        result(save(key, value: args["value"] as? String ?? ""))
      case "delete":
        delete(key)
        result(nil)
      default:
        result(FlutterMethodNotImplemented)
      }
    }
  }

  private static func base(_ key: String) -> [String: Any] {
    [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: key,
    ]
  }

  private static func has(_ key: String) -> Bool {
    var query = base(key)
    query[kSecReturnAttributes as String] = true
    return SecItemCopyMatching(query as CFDictionary, nil) == errSecSuccess
  }

  private static func read(_ key: String) -> String? {
    var query = base(key)
    query[kSecReturnData as String] = true
    var item: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
      let data = item as? Data
    else {
      return nil
    }
    return String(data: data, encoding: .utf8)
  }

  private static func save(_ key: String, value: String) -> Bool {
    guard !value.isEmpty else { return false }
    let data = Data(value.utf8)
    let updateStatus = SecItemUpdate(
      base(key) as CFDictionary,
      [
        kSecValueData as String: data,
        kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
      ] as CFDictionary
    )
    if updateStatus == errSecSuccess {
      return true
    }
    if updateStatus != errSecItemNotFound {
      return false
    }
    var query = base(key)
    query[kSecValueData as String] = data
    query[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    return SecItemAdd(query as CFDictionary, nil) == errSecSuccess
  }

  private static func delete(_ key: String) {
    SecItemDelete(base(key) as CFDictionary)
  }
}
