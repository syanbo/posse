import 'dart:async';

import 'package:flutter/services.dart';

import 'keys.dart';

/// 原生层每 200 ms 推一次。帧率是最近一秒的帧数；码率是比特每秒、只算视频本身，平均取最近 2 秒，
/// 峰值是 10 秒里最满的 250 ms；丢包和解码耗时是从开看以来的累计和最近一秒的中位数。
class DeskStats {
  const DeskStats({
    this.fps = 0,
    this.received = 0,
    this.still = false,
    this.bitrate = 0,
    this.peak = 0,
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

  /// 解码出来的帧率。
  final double fps;

  /// 收到、拼成整帧的帧率，比 fps 高得多说明解码跟不上。
  final double received;

  /// 1 秒以上没有新帧：Mac 只在画面变了时出帧，这是画面没动。
  final bool still;

  final double bitrate;
  final double peak;
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
      received: n(map['received']),
      still: map['still'] == true,
      bitrate: n(map['bitrate']),
      peak: n(map['peak']),
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

/// 换成字节每秒，好和测速软件的 MB/s 对上。
String deskByteRate(double bps) {
  final bytes = bps / 8;
  if (bytes >= 1000000) return '${(bytes / 1000000).toStringAsFixed(1)} MB/s';
  return '${(bytes / 1000).toStringAsFixed(0)} KB/s';
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
    : _channel = channel ?? const MethodChannel('posse/desk'),
      _events = events ?? const EventChannel('posse/desk/stats');

  final MethodChannel _channel;
  final EventChannel _events;

  Stream<DeskStats>? _stats;

  Stream<DeskStats> stats() =>
      _stats ??= _events.receiveBroadcastStream().map(DeskStats.parse);

  /// host 是直连地址，经隧道连着又找不到局域网地址时为空，只走中转（relay 是 host:port）。
  /// nack 是电脑认缺包点名补发（ready 里的 nack），旧电脑不认，缺包直接要关键帧。
  Future<int> start({
    String? host,
    int? port,
    String? relay,
    Uint8List? relayToken,
    required Uint8List keyToPhone,
    required Uint8List keyToMac,
    DeskCodec codec = DeskCodec.hevc,
    bool nack = false,
  }) async {
    final id = await _channel.invokeMethod<int>('start', {
      'host': ?host,
      'port': ?port,
      'relay': ?relay,
      'relayToken': ?relayToken,
      'keyToPhone': keyToPhone,
      'keyToMac': keyToMac,
      'codec': codec.name,
      'nack': nack,
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
    : _channel = channel ?? const MethodChannel('posse/desk/edges');

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
