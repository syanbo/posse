import Flutter
import UIKit

/// 看桌面时把屏幕顶边和底边的系统手势延后：手指从边缘拖画面，
/// 系统要等这一下没被界面吃掉才拉通知中心或回主屏幕。
/// Flutter 没有这个开关，故事板里的根控制器用这一层，离开页面改回空。
/// 看桌面的方向也锁在这一层（[DeskOrientation]）。
@objc(HiveFlutterViewController)
final class HiveFlutterViewController: FlutterViewController {
  static var deskDefersEdges = false
  static var deskOrientations: UIInterfaceOrientationMask?

  override var supportedInterfaceOrientations: UIInterfaceOrientationMask {
    Self.deskOrientations ?? super.supportedInterfaceOrientations
  }

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

/// 看桌面锁方向、离开时放开。不用 SystemChrome：放开时它把「全部方向」交给 requestGeometryUpdate，
/// 系统会先转到横屏再按传感器转回竖屏，回到首页闪一下横屏。这里放开只让系统重新问支持的方向，按手机实际朝向转。
enum DeskOrientation {
  static func install(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(
      name: "hive/desk/orientation",
      binaryMessenger: messenger
    )
    channel.setMethodCallHandler { call, result in
      let mask: UIInterfaceOrientationMask?
      switch call.method {
      case "lock":
        mask = (call.arguments as? Bool ?? false) ? .landscape : .portrait
      case "release":
        mask = nil
      default:
        result(FlutterMethodNotImplemented)
        return
      }
      DispatchQueue.main.async {
        HiveFlutterViewController.deskOrientations = mask
        for scene in UIApplication.shared.connectedScenes {
          guard let windowScene = scene as? UIWindowScene else { continue }
          for window in windowScene.windows {
            if #available(iOS 16.0, *) {
              window.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
            }
          }
          if let mask {
            rotate(windowScene, to: mask)
          }
        }
        if mask == nil, #unavailable(iOS 16.0) {
          UIViewController.attemptRotationToDeviceOrientation()
        }
        result(nil)
      }
    }
  }

  /// 锁的方向不含现在的方向时转过去。
  private static func rotate(_ scene: UIWindowScene, to mask: UIInterfaceOrientationMask) {
    if mask.contains(UIInterfaceOrientationMask(rawValue: 1 << scene.interfaceOrientation.rawValue)) {
      return
    }
    if #available(iOS 16.0, *) {
      scene.requestGeometryUpdate(.iOS(interfaceOrientations: mask)) { error in
        NSLog("desk: rotate failed: %@", error.localizedDescription)
      }
    } else {
      let target: UIInterfaceOrientation = mask == .portrait ? .portrait : .landscapeRight
      UIDevice.current.setValue(target.rawValue, forKey: "orientation")
      UIViewController.attemptRotationToDeviceOrientation()
    }
  }
}
