import 'dart:async';

import 'package:flutter/services.dart';

/// 推送的原生一侧（iOS 的 PushPlugin.swift）：通知权限、APNs token、推送密钥、角标、点过的通知。
/// 方案见 docs/plans/push.md。测试里换成假的。
class PushAuthorization {
  const PushAuthorization(
    this.status, {
    this.alerts = false,
    this.badge = false,
  });

  /// notDetermined、denied 或 authorized；系统的横幅和角标还可分别关闭。
  final String status;
  final bool alerts;
  final bool badge;
}

abstract class PushPlatform {
  Future<PushAuthorization> authorization();
  Future<bool> request();

  /// APNs 设备 token（hex）。
  Future<String> token();

  /// 加密通知内容的密钥（base64），通知扩展从 App Group 里读同一把。
  Future<String> key();
  Future<bool> sandbox();
  Future<void> setLanguage(String tag);
  Future<void> setBadge(int count);
  Future<void> configure({required bool alerts, required bool badge});
  Future<void> openSettings();

  /// 点过、还没处理的通知：c 电脑公钥、wt 工作区、tab 终端 ID。
  Future<Map<String, String>?> takeOpened();

  /// 用户点了通知，之后调 [takeOpened] 取。
  set onOpened(void Function()? callback);
}

class ChannelPush implements PushPlatform {
  static const _channel = MethodChannel('posse/push');
  void Function()? _opened;

  // 不在构造函数里设：模型在 runApp 之前建，那时 binding 还没初始化，设了会抛异常、App 停在启动画面
  @override
  set onOpened(void Function()? callback) {
    _opened = callback;
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'opened') _opened?.call();
    });
  }

  @override
  Future<PushAuthorization> authorization() async {
    final value = await _channel.invokeMapMethod<String, Object?>(
      'authorization',
    );
    return PushAuthorization(
      value?['status'] as String? ?? 'denied',
      alerts: value?['alerts'] == true,
      badge: value?['badge'] == true,
    );
  }

  @override
  Future<void> configure({required bool alerts, required bool badge}) =>
      _channel.invokeMethod<void>('configure', {
        'alerts': alerts,
        'badge': badge,
      });

  @override
  Future<void> openSettings() => _channel.invokeMethod<void>('openSettings');

  @override
  Future<bool> request() async =>
      await _channel.invokeMethod<bool>('request') ?? false;

  @override
  Future<String> token() async {
    final t = await _channel
        .invokeMethod<String>('token')
        .timeout(const Duration(seconds: 20));
    if (t == null || t.isEmpty) throw StateError('no APNs token');
    return t;
  }

  @override
  Future<String> key() async {
    final k = await _channel.invokeMethod<String>('key');
    if (k == null || k.isEmpty) throw StateError('no push key');
    return k;
  }

  @override
  Future<bool> sandbox() async =>
      await _channel.invokeMethod<bool>('sandbox') ?? false;

  @override
  Future<void> setLanguage(String tag) =>
      _channel.invokeMethod<void>('setLanguage', tag);

  @override
  Future<void> setBadge(int count) =>
      _channel.invokeMethod<void>('setBadge', count);

  @override
  Future<Map<String, String>?> takeOpened() async {
    final raw = await _channel.invokeMethod<Map<Object?, Object?>>(
      'takeOpened',
    );
    if (raw == null) return null;
    return {
      for (final MapEntry(:key, :value) in raw.entries)
        if (key is String && value is String) key: value,
    };
  }
}

/// 设置页显示的推送状态。
enum PushStatus {
  /// 这台设备不支持（安卓、测试）
  unsupported,

  /// 通知和角标都不需要远程更新。
  off,

  /// 中转尚不支持只更新角标的推送。
  unsupportedRelay,

  /// 系统设置里没允许通知
  denied,

  /// 已经向电脑注册，电脑配了外网中转
  on,

  /// 注册了，但电脑没配外网中转，推不出来
  noRelay,

  /// 电脑的常驻进程太旧，不认推送
  unsupportedComputer,

  /// 拿不到 token 或注册失败
  failed,

  /// 还没连上电脑注册过
  unknown,
}
