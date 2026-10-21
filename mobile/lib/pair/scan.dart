import 'dart:async';
import 'dart:io';

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

enum ScanEnd { code, cancelled, denied, failed, manual, unsupported }

class ScanPick {
  const ScanPick.code(this.code) : end = ScanEnd.code;
  const ScanPick.end(this.end) : code = null;

  final String? code;
  final ScanEnd end;
}

/// 要权限、开相机、等扫到配对码，几个入口共用。只有 iOS 有原生扫码。
Future<ScanPick> scanPairingCode(BuildContext context) async {
  if (!Platform.isIOS) return const ScanPick.end(ScanEnd.unsupported);
  bool granted;
  try {
    granted = await qrChannel.invokeMethod<bool>('authorize') ?? false;
  } on MissingPluginException {
    return const ScanPick.end(ScanEnd.unsupported);
  } on PlatformException {
    return const ScanPick.end(ScanEnd.failed);
  }
  if (!granted) return const ScanPick.end(ScanEnd.denied);
  if (!context.mounted) return const ScanPick.end(ScanEnd.cancelled);
  final pick = await Navigator.of(context).push<ScanPick>(
    MaterialPageRoute(
      fullscreenDialog: true,
      builder: (_) => const QrScanPage(),
    ),
  );
  return pick ?? const ScanPick.end(ScanEnd.cancelled);
}

class QrScanPage extends StatefulWidget {
  const QrScanPage({super.key});

  @override
  State<QrScanPage> createState() => _QrScanPageState();
}

class _QrScanPageState extends State<QrScanPage> {
  MethodChannel? _channel;
  late final ScanSession _session;
  String? _note;
  Animation<double>? _enter;
  var _shown = false;

  @override
  void initState() {
    super.initState();
    _session = ScanSession(
      stop: () {
        unawaited(_channel?.invokeMethod<void>('stop'));
      },
      connect: (code) {
        if (!mounted) return;
        unawaited(HapticFeedback.mediumImpact());
        Navigator.pop(context, ScanPick.code(code));
      },
    );
  }

  // 相机画面在弹出动画结束后才上屏：平台视图跟着滑动时 iOS 每帧都要重新合成它，会卡。
  // 视图在 Offstage 里照样创建，相机在动画期间就开始启动，不多等。
  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final enter = ModalRoute.of(context)?.animation;
    if (enter == _enter) return;
    _enter?.removeStatusListener(_onEnter);
    _enter = enter;
    if (enter == null || enter.isCompleted) {
      _shown = true;
    } else {
      enter.addStatusListener(_onEnter);
    }
  }

  void _onEnter(AnimationStatus status) {
    if (status != AnimationStatus.completed) return;
    _enter?.removeStatusListener(_onEnter);
    setState(() => _shown = true);
  }

  @override
  void dispose() {
    _enter?.removeStatusListener(_onEnter);
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
          _finish(const ScanPick.end(ScanEnd.failed));
      }
    });
  }

  void _finish(ScanPick pick) {
    if (!mounted || _session.closed) return;
    _session.closed = true;
    unawaited(_channel?.invokeMethod<void>('stop'));
    Navigator.pop(context, pick);
  }

  /// 剪贴板里是配对码就直接用，不是就在取景框下面说一声，相机接着扫。
  Future<void> _paste() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final text = data?.text?.trim() ?? '';
    if (!mounted) return;
    if (isPairingCode(text)) {
      _finish(ScanPick.code(text));
      return;
    }
    setState(() => _note = text.isEmpty ? '剪贴板是空的' : '剪贴板里不是配对码');
  }

  @override
  Widget build(BuildContext context) {
    const hint = TextStyle(color: Colors.white, fontSize: 15, height: 1.4);
    return Scaffold(
      backgroundColor: Colors.black,
      extendBodyBehindAppBar: true,
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        foregroundColor: Colors.white,
        leading: IconButton(
          tooltip: '关闭',
          icon: const Icon(Icons.close_rounded),
          onPressed: () => _finish(const ScanPick.end(ScanEnd.cancelled)),
        ),
      ),
      body: Stack(
        fit: StackFit.expand,
        children: [
          Offstage(
            offstage: !_shown,
            child: UiKitView(
              viewType: 'hive/qr',
              onPlatformViewCreated: _attach,
            ),
          ),
          LayoutBuilder(
            builder: (context, box) {
              final side = (box.maxWidth * 0.68).clamp(200.0, 320.0);
              final frame = Rect.fromCenter(
                center: Offset(box.maxWidth / 2, box.maxHeight * 0.42),
                width: side,
                height: side,
              );
              return Stack(
                children: [
                  Positioned.fill(
                    child: IgnorePointer(
                      child: CustomPaint(painter: _FramePainter(frame)),
                    ),
                  ),
                  Positioned(
                    left: 24,
                    right: 24,
                    bottom: box.maxHeight - frame.top + 20,
                    child: const Text(
                      '对准电脑上 Hive 的配对码',
                      textAlign: TextAlign.center,
                      style: hint,
                    ),
                  ),
                  if (_note != null)
                    Positioned(
                      left: 24,
                      right: 24,
                      top: frame.bottom + 20,
                      child: Text(
                        _note!,
                        textAlign: TextAlign.center,
                        style: hint.copyWith(color: Colors.white70),
                      ),
                    ),
                ],
              );
            },
          ),
          Align(
            alignment: Alignment.bottomCenter,
            child: SafeArea(
              minimum: const EdgeInsets.only(bottom: 16),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  _action(Icons.content_paste_rounded, '粘贴配对码', _paste),
                  const SizedBox(width: 12),
                  _action(
                    Icons.keyboard_rounded,
                    '手动输入',
                    () => _finish(const ScanPick.end(ScanEnd.manual)),
                  ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _action(IconData icon, String label, VoidCallback onTap) {
    return TextButton.icon(
      style: TextButton.styleFrom(
        foregroundColor: Colors.white,
        backgroundColor: Colors.white.withValues(alpha: 0.16),
        shape: const StadiumBorder(),
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
      ),
      onPressed: onTap,
      icon: Icon(icon, size: 18),
      label: Text(label),
    );
  }
}

/// 取景框外压暗，四角画短线。识别不限于框内，框只是告诉人往哪儿对。
class _FramePainter extends CustomPainter {
  _FramePainter(this.frame);

  final Rect frame;

  @override
  void paint(Canvas canvas, Size size) {
    final hole = RRect.fromRectAndRadius(frame, const Radius.circular(16));
    canvas.drawPath(
      Path.combine(
        PathOperation.difference,
        Path()..addRect(Offset.zero & size),
        Path()..addRRect(hole),
      ),
      Paint()..color = Colors.black.withValues(alpha: 0.5),
    );
    final pen = Paint()
      ..color = Colors.white
      ..style = PaintingStyle.stroke
      ..strokeWidth = 3
      ..strokeCap = StrokeCap.round;
    const arm = 26.0;
    const r = 16.0;
    final l = frame.left, t = frame.top, rt = frame.right, b = frame.bottom;
    for (final corner in [
      Path()
        ..moveTo(l, t + arm)
        ..lineTo(l, t + r)
        ..arcToPoint(Offset(l + r, t), radius: const Radius.circular(r))
        ..lineTo(l + arm, t),
      Path()
        ..moveTo(rt - arm, t)
        ..lineTo(rt - r, t)
        ..arcToPoint(Offset(rt, t + r), radius: const Radius.circular(r))
        ..lineTo(rt, t + arm),
      Path()
        ..moveTo(rt, b - arm)
        ..lineTo(rt, b - r)
        ..arcToPoint(Offset(rt - r, b), radius: const Radius.circular(r))
        ..lineTo(rt - arm, b),
      Path()
        ..moveTo(l + arm, b)
        ..lineTo(l + r, b)
        ..arcToPoint(Offset(l, b - r), radius: const Radius.circular(r))
        ..lineTo(l, b - arm),
    ]) {
      canvas.drawPath(corner, pen);
    }
  }

  @override
  bool shouldRepaint(_FramePainter old) => old.frame != frame;
}
