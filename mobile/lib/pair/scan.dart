import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../protocol/pairing.dart';

const qrChannel = MethodChannel('hive/qr');

String qrViewChannel(int id) => 'hive/qr/$id';

/// 扫到的文字是不是配对码。解析失败就当没看见，相机继续开。
bool isPairingCode(String raw) {
  try {
    PairingOffer.parse(raw);
    return true;
  } catch (_) {
    return false;
  }
}

/// 连续上报里只认第一次合法配对码。
class ScanGate {
  var _accepted = false;

  /// 非空表示该停相机并把这串交给连接。
  String? consider(String raw) {
    if (_accepted) return null;
    final text = raw.trim();
    if (!isPairingCode(text)) return null;
    _accepted = true;
    return text;
  }
}

/// 认到配对码才停相机并连接；其他字符串不动这两件事。
class ScanSession {
  ScanSession({required this.stop, required this.connect});

  final void Function() stop;
  final void Function(String code) connect;
  final ScanGate _gate = ScanGate();
  var closed = false;

  void onCode(String raw) {
    if (closed) return;
    final code = _gate.consider(raw);
    if (code == null) return;
    closed = true;
    stop();
    connect(code);
  }
}

class ScanPick {
  const ScanPick.code(this.code) : failed = false;
  const ScanPick.failed() : code = null, failed = true;

  final String? code;
  final bool failed;
}

class QrScanPage extends StatefulWidget {
  const QrScanPage({super.key});

  @override
  State<QrScanPage> createState() => _QrScanPageState();
}

class _QrScanPageState extends State<QrScanPage> {
  MethodChannel? _channel;
  late final ScanSession _session;

  @override
  void initState() {
    super.initState();
    _session = ScanSession(
      stop: () {
        unawaited(_channel?.invokeMethod<void>('stop'));
      },
      connect: (code) {
        if (!mounted) return;
        Navigator.pop(context, ScanPick.code(code));
      },
    );
  }

  @override
  void dispose() {
    unawaited(_channel?.invokeMethod<void>('stop'));
    super.dispose();
  }

  void _attach(int id) {
    final channel = MethodChannel(qrViewChannel(id));
    _channel = channel;
    channel.setMethodCallHandler((call) async {
      switch (call.method) {
        case 'code':
          _session.onCode(call.arguments as String? ?? '');
        case 'failed':
          if (!mounted || _session.closed) return;
          _session.closed = true;
          unawaited(channel.invokeMethod<void>('stop'));
          Navigator.pop(context, const ScanPick.failed());
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.black,
      appBar: AppBar(
        backgroundColor: Colors.black,
        foregroundColor: Colors.white,
        title: const Text('对准电脑上的配对码'),
      ),
      body: UiKitView(viewType: 'hive/qr', onPlatformViewCreated: _attach),
    );
  }
}
