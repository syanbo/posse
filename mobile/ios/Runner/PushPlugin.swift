import Flutter
import Security
import UIKit
import UserNotifications

/// 推送通道 posse/push：通知权限、APNs token、推送密钥（存在 App Group，通知扩展解密用）、
/// 点通知打开会话。方案见 docs/plans/push.md。
final class PushPlugin: NSObject, UNUserNotificationCenterDelegate {
  static let shared = PushPlugin()
  static let group = "group.com.neolabsapp.posse.mobile"

  private var channel: FlutterMethodChannel?
  private var token: String?
  private var waiting: [FlutterResult] = []
  /// 点过、Dart 还没取走的通知（冷启动时 Dart 还没起来）
  private var opened: [String: Any]?

  func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(name: "posse/push", binaryMessenger: messenger)
    self.channel = channel
    channel.setMethodCallHandler { [weak self] call, result in
      self?.handle(call, result)
    }
  }

  private var defaults: UserDefaults? { UserDefaults(suiteName: PushPlugin.group) }

  private func handle(_ call: FlutterMethodCall, _ result: @escaping FlutterResult) {
    let center = UNUserNotificationCenter.current()
    switch call.method {
    case "authorization":
      center.getNotificationSettings { settings in
        let status: String
        switch settings.authorizationStatus {
        case .notDetermined: status = "notDetermined"
        case .denied: status = "denied"
        default: status = "authorized"
        }
        DispatchQueue.main.async {
          result([
            "status": status,
            "alerts": settings.alertSetting == .enabled || settings.soundSetting == .enabled,
            "badge": settings.badgeSetting == .enabled,
          ])
        }
      }
    case "request":
      center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
        DispatchQueue.main.async { result(granted) }
      }
    case "token":
      if let token {
        result(token)
        return
      }
      waiting.append(result)
      UIApplication.shared.registerForRemoteNotifications()
    case "key":
      result(pushKey())
    case "sandbox":
      result(PushPlugin.sandbox)
    case "setLanguage":
      defaults?.set(call.arguments as? String ?? "", forKey: "pushLang")
      result(nil)
    case "configure":
      let options = call.arguments as? [String: Bool] ?? [:]
      defaults?.set(options["alerts"] ?? true, forKey: "pushAlerts")
      defaults?.set(options["badge"] ?? true, forKey: "pushBadge")
      result(nil)
    case "openSettings":
      let path: String
      if #available(iOS 16.0, *) {
        path = UIApplication.openNotificationSettingsURLString
      } else {
        path = UIApplication.openSettingsURLString
      }
      if let url = URL(string: path) { UIApplication.shared.open(url) }
      result(nil)
    case "setBadge":
      let n = call.arguments as? Int ?? 0
      if #available(iOS 16.0, *) {
        center.setBadgeCount(n) { _ in }
      } else {
        UIApplication.shared.applicationIconBadgeNumber = n
      }
      result(nil)
    case "takeOpened":
      result(opened)
      opened = nil
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  /// token 属于哪个 APNs 环境看签名用的描述文件，不看编译配置：开发证书签的 Release 包拿到的也是沙盒 token。
  /// App Store 的包里没有 embedded.mobileprovision，是正式环境。
  static let sandbox: Bool = {
    guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
      let data = try? Data(contentsOf: url),
      // 描述文件是 CMS 签名包着的 XML plist，按文本找到 aps-environment 的值
      let text = String(data: data, encoding: .isoLatin1),
      let key = text.range(of: "<key>aps-environment</key>"),
      let open = text.range(of: "<string>", range: key.upperBound..<text.endIndex),
      let close = text.range(of: "</string>", range: open.upperBound..<text.endIndex)
    else { return false }
    return text[open.upperBound..<close.lowerBound] == "development"
  }()

  /// 推送密钥：第一次用到时生成 32 字节随机数，存在 App Group 里，之后一直用它。
  private func pushKey() -> String? {
    if let key = defaults?.string(forKey: "pushKey") {
      return key
    }
    var bytes = [UInt8](repeating: 0, count: 32)
    guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
      return nil
    }
    let key = Data(bytes).base64EncodedString()
    defaults?.set(key, forKey: "pushKey")
    return key
  }

  func registered(_ deviceToken: Data) {
    let hex = deviceToken.map { String(format: "%02x", $0) }.joined()
    token = hex
    let pending = waiting
    waiting = []
    pending.forEach { $0(hex) }
  }

  func failed(_ error: Error) {
    let pending = waiting
    waiting = []
    pending.forEach {
      $0(FlutterError(code: "register_failed", message: error.localizedDescription, details: nil))
    }
  }

  // App 在前台时也显示横幅：Mac 只在手机没开着 App（或开着的不是它）时才推
  func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    willPresent notification: UNNotification,
    withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
  ) {
    var options: UNNotificationPresentationOptions = []
    if defaults?.object(forKey: "pushAlerts") as? Bool != false {
      options.formUnion([.banner, .list, .sound])
    }
    // 前台角标由当前电脑的会话列表维护，不能让其他电脑或迟到的推送覆盖它。
    completionHandler(options)
  }

  func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    didReceive response: UNNotificationResponse,
    withCompletionHandler completionHandler: @escaping () -> Void
  ) {
    if let posse = response.notification.request.content.userInfo["posse"] as? [String: Any] {
      opened = posse
      channel?.invokeMethod("opened", arguments: nil)
    }
    completionHandler()
  }
}
