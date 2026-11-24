import Flutter
import UIKit
import UserNotifications

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var qrChannel: FlutterMethodChannel?
  private var tsChannel: FlutterMethodChannel?
  private var desk: DeskPlugin?

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    // 在启动时就设好：冷启动时点的通知要交给它
    UNUserNotificationCenter.current().delegate = PushPlugin.shared
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  override func application(
    _ application: UIApplication,
    didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
  ) {
    PushPlugin.shared.registered(deviceToken)
  }

  override func application(
    _ application: UIApplication,
    didFailToRegisterForRemoteNotificationsWithError error: Error
  ) {
    PushPlugin.shared.failed(error)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    let registrar = engineBridge.applicationRegistrar
    registrar.register(
      QrScanViewFactory(messenger: registrar.messenger()),
      withId: "posse/qr"
    )
    let channel = FlutterMethodChannel(
      name: "posse/qr",
      binaryMessenger: registrar.messenger()
    )
    qrChannel = channel
    let tsChannel = FlutterMethodChannel(
      name: "posse/tailscale",
      binaryMessenger: registrar.messenger()
    )
    self.tsChannel = tsChannel
    tsChannel.setMethodCallHandler { call, result in
      if call.method == "stateDir" {
        do {
          result(try Self.tailscaleStateDir())
        } catch {
          result(FlutterError(code: "failed", message: error.localizedDescription, details: nil))
        }
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
    let desk = DeskPlugin(textures: registrar.textures(), messenger: registrar.messenger())
    desk.install()
    self.desk = desk
    PushPlugin.shared.install(messenger: registrar.messenger())
    BackgroundPlugin.shared.install(messenger: registrar.messenger())
    channel.setMethodCallHandler { call, result in
      if call.method == "authorize" {
        QrCamera.authorize(result)
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
  }

  /// 创建并配置内置 Tailscale 状态目录：
  /// 1) 在持久化父目录 posse-nobackup 上设置排除 iCloud 备份与 completeUntilFirstUserAuthentication，
  ///    即使 Reset 删除空叶子目录 tailscale 后再重建，父目录的防备份与保护属性依然保留；
  /// 2) 同步对叶子目录本身施加相同属性。
  private static func tailscaleStateDir() throws -> String {
    let fm = FileManager.default
    let base = try fm.url(
      for: .applicationSupportDirectory,
      in: .userDomainMask,
      appropriateFor: nil,
      create: true
    )
    var parent = base.appendingPathComponent("posse-nobackup", isDirectory: true)
    var dir = parent.appendingPathComponent("tailscale", isDirectory: true)
    let attrs: [FileAttributeKey: Any] = [
      .protectionKey: FileProtectionType.completeUntilFirstUserAuthentication
    ]
    for var target in [parent, dir] {
      try fm.createDirectory(at: target, withIntermediateDirectories: true, attributes: attrs)
      var values = URLResourceValues()
      values.isExcludedFromBackup = true
      try target.setResourceValues(values)
      try fm.setAttributes(attrs, ofItemAtPath: target.path)
    }
    return dir.path
  }
}
