import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// 后台保活只在 iOS 上有（BackgroundPlugin.swift）：安卓切到后台连接本来就留着。
bool get backgroundKeepSupported =>
    !kIsWeb && defaultTargetPlatform == TargetPlatform.iOS;

const _channel = MethodChannel('posse/background');
bool? _sent;

/// 把「后台保持连接」交给原生一侧，和上次一样就不发。
void syncKeepAlive(bool on) {
  if (!backgroundKeepSupported || _sent == on) return;
  _sent = on;
  _channel.invokeMethod<void>('setKeepAlive', on).catchError((Object _) {});
}
