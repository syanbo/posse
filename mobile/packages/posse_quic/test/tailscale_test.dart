import 'dart:async';
import 'dart:io';

import 'package:posse_quic/posse_quic.dart';
import 'package:test/test.dart';

void main() {
  test('tailscale FFI validation, updates stream, and lifecycle', () async {
    expect(TailscaleNode.status().state, 'disabled');

    // 先订阅状态流，验证首次注册回调立刻推送初始 disabled 快照，后续启动与停止按顺序送达
    final seen = <TailscaleStatus>[];
    final gotInitial = Completer<void>();
    final gotEnabled = Completer<void>();
    final gotStoppedAfterEnable = Completer<void>();
    final sub = TailscaleNode.updates.listen((st) {
      seen.add(st);
      if (!gotInitial.isCompleted && st.state == 'disabled') {
        gotInitial.complete();
      }
      if (!gotEnabled.isCompleted && st.enabled) {
        gotEnabled.complete();
      }
      if (gotEnabled.isCompleted &&
          !gotStoppedAfterEnable.isCompleted &&
          !st.enabled &&
          st.state == 'disabled') {
        gotStoppedAfterEnable.complete();
      }
    });
    addTearDown(sub.cancel);
    await gotInitial.future.timeout(const Duration(seconds: 3));

    // 未启动和非 Tailscale 地址拨号/开桥快速报错
    await expectLater(
      QuicConnection.dialTailscale(
        '192.168.1.5',
        6789,
        timeout: const Duration(seconds: 2),
      ),
      throwsA(isA<QuicException>()),
    );
    await expectLater(
      QuicConnection.dialTailscale(
        '::1',
        6789,
        timeout: const Duration(seconds: 2),
      ),
      throwsA(isA<QuicException>()),
    );
    await expectLater(
      QuicConnection.dialTailscale(
        '100.64.0.1',
        6789,
        timeout: const Duration(seconds: 2),
      ),
      throwsA(isA<QuicException>()),
    );
    await expectLater(
      TailscaleNode.openUdpBridge('192.168.1.5', 6789),
      throwsA(isA<TailscaleException>()),
    );
    await expectLater(
      TailscaleNode.openUdpBridge('100.64.0.1', 6789),
      throwsA(isA<TailscaleException>()),
    );
    final route = await TailscaleNode.peerRoute('100.64.0.1');
    expect(route.kind, 'unknown');

    // 非法参数拒绝
    await expectLater(
      TailscaleNode.start(stateDir: 'relative/path'),
      throwsA(isA<TailscaleException>()),
    );

    final dir = await Directory.systemTemp.createTemp('posse-ts-ffi-test-');
    addTearDown(() async {
      await TailscaleNode.stop();
      if (dir.existsSync()) dir.deleteSync(recursive: true);
    });

    await expectLater(
      TailscaleNode.start(stateDir: dir.path, controlUrl: 'ftp://invalid'),
      throwsA(isA<TailscaleException>()),
    );

    final started = await TailscaleNode.start(
      stateDir: dir.path,
      hostname: 'posse-dart-test',
      controlUrl: 'http://127.0.0.1:1',
      disableBindInterface: true,
    );
    expect(started.enabled, isTrue);
    expect(started.hostname, 'posse-dart-test');
    expect(started.controlUrl, 'http://127.0.0.1:1');
    expect(started.disableBindInterface, isTrue);

    await gotEnabled.future.timeout(const Duration(seconds: 3));
    TailscaleNode.wake();

    final stopped = await TailscaleNode.stop();
    expect(stopped.enabled, isFalse);
    expect(stopped.state, 'disabled');

    await gotStoppedAfterEnable.future.timeout(const Duration(seconds: 3));
    expect(seen.first.state, 'disabled');
    expect(seen.last.state, 'disabled');
    expect(seen.any((s) => s.enabled), isTrue);

    final reset = await TailscaleNode.reset(stateDir: dir.path);
    expect(reset.localCleared, isTrue);
    expect(reset.status.enabled, isFalse);
  });
}
