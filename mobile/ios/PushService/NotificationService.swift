import CryptoKit
import Foundation
import UserNotifications

/// 解开 Mac 加密的推送内容（AES-256-GCM，密钥是 App 生成、存在 App Group 里的推送密钥），
/// 按 App 里选的界面语言写成标题和正文。解不开时留兜底文字。方案见 docs/plans/push.md。
final class NotificationService: UNNotificationServiceExtension {
  private var handler: ((UNNotificationContent) -> Void)?
  private var content: UNMutableNotificationContent?

  override func didReceive(
    _ request: UNNotificationRequest,
    withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
  ) {
    handler = contentHandler
    let content =
      (request.content.mutableCopy() as? UNMutableNotificationContent)
      ?? UNMutableNotificationContent()
    self.content = content
    // 已经在路上的通知也不能在关闭角标后把旧计数带回来。
    if UserDefaults(suiteName: pushGroup)?.object(forKey: "pushBadge") as? Bool == false {
      content.badge = 0
    }
    let strings = PushStrings()
    content.title = "Posse"
    content.body = strings.text("fallbackBody")
    if let body = PushBody.open(request.content.userInfo) {
      body.fill(content, strings)
    }
    contentHandler(content)
  }

  override func serviceExtensionTimeWillExpire() {
    if let handler, let content {
      handler(content)
    }
  }
}

let pushGroup = "group.com.neolabsapp.posse.mobile"

/// 通知扩展用的文字：语言跟 App 的设置走（App 写进 App Group 的 pushLang），没写过跟系统。
struct PushStrings {
  let bundle: Bundle

  init() {
    let defaults = UserDefaults(suiteName: pushGroup)
    var lang = defaults?.string(forKey: "pushLang") ?? ""
    if lang.isEmpty {
      lang = Locale.preferredLanguages.first ?? "en"
    }
    let folder = lang.hasPrefix("zh") ? "zh-Hans" : "en"
    let main = Bundle(for: NotificationService.self)
    if let path = main.path(forResource: folder, ofType: "lproj"), let b = Bundle(path: path) {
      bundle = b
    } else {
      bundle = main
    }
  }

  func text(_ key: String) -> String {
    bundle.localizedString(forKey: key, value: key, table: nil)
  }
}

/// Mac 加密的内容，字段见 internal/mobile/push.go 的 pushBody。
struct PushBody {
  let kind: String
  let agent: String
  let project: String
  let space: String
  let tab: String
  let message: String
  let computer: String
  let name: String
  let worktree: String
  let terminal: String

  static func open(_ info: [AnyHashable: Any]) -> PushBody? {
    guard let sealed = info["e"] as? String,
      let data = Data(base64Encoded: sealed),
      let keyText = UserDefaults(suiteName: pushGroup)?.string(forKey: "pushKey"),
      let key = Data(base64Encoded: keyText), key.count == 32,
      let box = try? AES.GCM.SealedBox(combined: data),
      let plain = try? AES.GCM.open(box, using: SymmetricKey(data: key)),
      let json = try? JSONSerialization.jsonObject(with: plain) as? [String: Any]
    else { return nil }
    func s(_ k: String) -> String { json[k] as? String ?? "" }
    return PushBody(
      kind: s("k"), agent: s("a"), project: s("p"), space: s("w"), tab: s("t"),
      message: s("m"), computer: s("c"), name: s("n"), worktree: s("wt"), terminal: s("tab"))
  }

  var agentTitle: String {
    switch agent {
    case "": return "Agent"
    case "grok": return "Grok"
    default: return agent.prefix(1).uppercased() + agent.dropFirst()
    }
  }

  func fill(_ content: UNMutableNotificationContent, _ strings: PushStrings) {
    let waiting = kind == "waiting"
    content.title = String(
      format: strings.text(waiting ? "waitingTitle" : "doneTitle"), agentTitle)
    content.subtitle = [project, space, tab].filter { !$0.isEmpty }.joined(separator: " · ")
    content.body = message.isEmpty ? strings.text(waiting ? "waitingBody" : "doneBody") : message
    content.threadIdentifier = worktree
    var info = content.userInfo
    info.removeValue(forKey: "e")
    info["posse"] = ["c": computer, "n": name, "wt": worktree, "tab": terminal]
    content.userInfo = info
  }
}
