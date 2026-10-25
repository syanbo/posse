import Flutter
import Security

/// 看桌面记住的 Mac 登录密码，按电脑（主机公钥）各存一条在钥匙串里：手机解锁时才读得出，
/// 只留在这台手机上（不进备份、不随迁移）。取的时候不另外验证：能打开 Posse 的人本来就能操作这台 Mac 的终端。
enum DeskPassword {
  private static let service = "com.dengbo.posse.desk-password"

  static func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(
      name: "posse/desk/password",
      binaryMessenger: messenger
    )
    channel.setMethodCallHandler { call, result in
      let args = call.arguments as? [String: Any] ?? [:]
      switch call.method {
      case "count":
        result(count())
        return
      case "forgetAll":
        SecItemDelete(
          [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
          ] as CFDictionary)
        result(nil)
        return
      default:
        break
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
        result(save(key, password: args["password"] as? String ?? ""))
      case "forget":
        forget(key)
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

  /// 记着几台电脑的密码。
  private static func count() -> Int {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecMatchLimit as String: kSecMatchLimitAll,
      kSecReturnAttributes as String: true,
    ]
    var items: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &items) == errSecSuccess else {
      return 0
    }
    return (items as? [Any])?.count ?? 0
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

  private static func save(_ key: String, password: String) -> Bool {
    guard !password.isEmpty else { return false }
    forget(key)
    var query = base(key)
    query[kSecValueData as String] = Data(password.utf8)
    query[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    return SecItemAdd(query as CFDictionary, nil) == errSecSuccess
  }

  private static func forget(_ key: String) {
    SecItemDelete(base(key) as CFDictionary)
  }
}
