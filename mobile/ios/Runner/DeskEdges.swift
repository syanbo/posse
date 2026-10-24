import Flutter
import UIKit

/// 看桌面时把屏幕顶边和底边的系统手势延后：手指从边缘拖画面，
/// 系统要等这一下没被界面吃掉才拉通知中心或回主屏幕。
/// Flutter 没有这个开关，故事板里的根控制器用这一层，离开页面改回空。
@objc(HiveFlutterViewController)
final class HiveFlutterViewController: FlutterViewController {
  static var deskDefersEdges = false

  override var preferredScreenEdgesDeferringSystemGestures: UIRectEdge {
    if Self.deskDefersEdges {
      return [.top, .bottom]
    }
    return super.preferredScreenEdgesDeferringSystemGestures
  }
}

enum DeskEdges {
  static func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(
      name: "hive/desk/edges",
      binaryMessenger: messenger
    )
    channel.setMethodCallHandler { call, result in
      guard call.method == "set" else {
        result(FlutterMethodNotImplemented)
        return
      }
      let on = call.arguments as? Bool ?? false
      DispatchQueue.main.async {
        HiveFlutterViewController.deskDefersEdges = on
        for scene in UIApplication.shared.connectedScenes {
          guard let windowScene = scene as? UIWindowScene else { continue }
          for window in windowScene.windows {
            window.rootViewController?
              .setNeedsUpdateOfScreenEdgesDeferringSystemGestures()
          }
        }
        result(nil)
      }
    }
  }
}
