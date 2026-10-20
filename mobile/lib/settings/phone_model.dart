import 'dart:io';

import 'package:device_info_plus/device_info_plus.dart';

/// 手机型号，电脑的「手机」页拿它当名字，如 iPhone 16 Pro、Pixel 8。
/// iOS 16 起不给用户起的设备名（要额外权限），用型号；Android 先用设置里的设备名。
Future<String> phoneModel() async {
  try {
    final info = DeviceInfoPlugin();
    if (Platform.isIOS) {
      final ios = await info.iosInfo;
      return ios.modelName.isNotEmpty ? ios.modelName : ios.model;
    }
    if (Platform.isAndroid) {
      final android = await info.androidInfo;
      if (android.name.isNotEmpty && android.name != android.model) {
        return android.name;
      }
      final brand = android.manufacturer;
      final model = android.model;
      if (brand.isEmpty ||
          model.toLowerCase().startsWith(brand.toLowerCase())) {
        return model;
      }
      return '${brand[0].toUpperCase()}${brand.substring(1)} $model'.trim();
    }
  } catch (_) {}
  return '';
}
