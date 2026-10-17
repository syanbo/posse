import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var qrChannel: FlutterMethodChannel?

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    let registrar = engineBridge.applicationRegistrar
    registrar.register(
      QrScanViewFactory(messenger: registrar.messenger()),
      withId: "hive/qr"
    )
    let channel = FlutterMethodChannel(
      name: "hive/qr",
      binaryMessenger: registrar.messenger()
    )
    qrChannel = channel
    channel.setMethodCallHandler { call, result in
      if call.method == "authorize" {
        QrCamera.authorize(result)
      } else {
        result(FlutterMethodNotImplemented)
      }
    }
  }
}
