import 'dart:async';

import 'package:flutter/services.dart';

import 'keys.dart';

/// 原生层每 200 ms 推一次。码率是比特每秒，丢包和解码耗时是从开看以来的累计和最近一秒的中位数。
class DeskStats {
  const DeskStats({
    this.fps = 0,
    this.bitrate = 0,
    this.loss = 0,
    this.decodeMs = 0,
    this.recoveries = 0,
    this.width = 0,
    this.height = 0,
    this.video = false,
    this.ack = 0,
    this.public,
    this.path,
  });

  final double fps;
  final double bitrate;
  final int loss;
  final double decodeMs;
  final int recoveries;
  final int width;
  final int height;
  final bool video;

  /// beat 里已经执行到的必达事件序号。
  final int ack;

  /// 画面经中转到达后，中转看到的接收 socket 的公网地址（ip:port），拿去和 Mac 交换、打洞。
  final String? public;

  /// 画面走哪条路：lan、relay、punched（打洞直连），还没有画面时为空。
  final String? path;

  static DeskStats parse(Object? raw) {
    final map = raw is Map ? raw : const {};
    double n(Object? value) => value is num ? value.toDouble() : 0;
    int i(Object? value) => value is num ? value.toInt() : 0;
    return DeskStats(
      fps: n(map['fps']),
      bitrate: n(map['bitrate']),
      loss: i(map['loss']),
      decodeMs: n(map['decodeMs']),
      recoveries: i(map['recoveries']),
      width: i(map['width']),
      height: i(map['height']),
      video: map['video'] == true,
      ack: i(map['ack']),
      public: map['public'] is String ? map['public'] as String : null,
      path: map['path'] is String ? map['path'] as String : null,
    );
  }
}

String deskBitrate(double bps) {
  if (bps >= 1000000) return '${(bps / 1000000).toStringAsFixed(1)} Mbps';
  if (bps >= 1000) return '${(bps / 1000).toStringAsFixed(0)} kbps';
  return '${bps.toStringAsFixed(0)} bps';
}

/// 现在连着的地址里的主机。桌面 UDP 不走 WebSocket 那个端口。
String? deskHostOf(String? url) {
  if (url == null || url.isEmpty) return null;
  final uri = Uri.tryParse(url);
  if (uri == null || uri.host.isEmpty) return null;
  return uri.host;
}

/// 视频包不到 Dart。这里把主机、端口和派生好的两把密钥交给插件，输入明文也从这里发出。
class DeskReceiver {
  DeskReceiver({MethodChannel? channel, EventChannel? events})
    : _channel = channel ?? const MethodChannel('hive/desk'),
      _events = events ?? const EventChannel('hive/desk/stats');

  final MethodChannel _channel;
  final EventChannel _events;

  Stream<DeskStats>? _stats;

  Stream<DeskStats> stats() =>
      _stats ??= _events.receiveBroadcastStream().map(DeskStats.parse);

  /// host 是直连地址，经隧道连着又找不到局域网地址时为空，只走中转（relay 是 host:port）。
  Future<int> start({
    String? host,
    int? port,
    String? relay,
    Uint8List? relayToken,
    required Uint8List keyToPhone,
    required Uint8List keyToMac,
    DeskCodec codec = DeskCodec.hevc,
  }) async {
    final id = await _channel.invokeMethod<int>('start', {
      'host': ?host,
      'port': ?port,
      'relay': ?relay,
      'relayToken': ?relayToken,
      'keyToPhone': keyToPhone,
      'keyToMac': keyToMac,
      'codec': codec.name,
    });
    if (id == null) throw StateError('desk start returned no texture');
    return id;
  }

  /// 一条已经编好的 input 明文。插件加密后发给 Mac。
  Future<void> sendInput(Uint8List plain) =>
      _channel.invokeMethod<void>('input', plain);

  /// 向 Mac 出口的公网地址（ip:port）打洞，见 desk.punch。
  Future<void> punch(String addr) => _channel.invokeMethod<void>('punch', addr);

  Future<void> stop() => _channel.invokeMethod<void>('stop');

  Future<void> pause() => _channel.invokeMethod<void>('pause');

  Future<void> resume() => _channel.invokeMethod<void>('resume');
}

/// 顶边和底边的系统手势延后。只在 iOS 插件里有实现。
class DeskEdges {
  DeskEdges({MethodChannel? channel})
    : _channel = channel ?? const MethodChannel('hive/desk/edges');

  final MethodChannel _channel;

  Future<void> set(bool defer) async {
    try {
      await _channel.invokeMethod<void>('set', defer);
    } on MissingPluginException {
      // 桌面插件只编进 iOS。
    } on PlatformException {
      // 根控制器还没接上时不挡离开页面。
    }
  }
}
