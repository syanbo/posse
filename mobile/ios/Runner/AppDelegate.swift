import Flutter
import UIKit
import UserNotifications

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var qrChannel: FlutterMethodChannel?
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
    let desk = DeskPlugin(textures: registrar.textures(), messenger: registrar.messenger())
    desk.install()
    self.desk = desk
    PushPlugin.shared.install(messenger: registrar.messenger())
    channel.setMethodCallHandler { call, result in
      if call.method == "authorize" {
        QrCamera.authorize(result)
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
  }
}
