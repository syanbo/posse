import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/media.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/media_pager.dart';
import 'package:posse_mobile/workspace/models.dart';

// 1×1 的 PNG
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
);

class _MediaRpc implements PhoneRpc {
  final calls = <String>[];
  final reads = <int>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    calls.add(method);
    switch (method) {
      case 'session.media':
        return {
          'made': [
            {
              'id': 'v1',
              'kind': 'video',
              'label': 'rec.mp4',
              'source': '/tmp/rec.mp4',
              'at': 1,
              'size': 10,
            },
            {
              'id': 's:1:0',
              'kind': 'image',
              'label': 'phone_screenshot',
              'source': 'mcp__iphone-use__phone_screenshot',
              'at': 2,
            },
          ],
          'madeAll': 41,
          'pasted': [
            {
              'id': '/tmp/1.png',
              'kind': 'image',
              'label': '#1',
              'source': '/tmp/1.png',
              'size': _png.length,
            },
          ],
        };
      case 'session.mediaThumb':
        return {'content': base64Encode(_png), 'width': 1, 'height': 1};
      case 'session.mediaRead':
        // 内嵌的图片列表里不带大小，第一段的回复才告诉手机
        final offset = params['offset'] as int;
        reads.add(offset);
        return {
          'content': base64Encode(Uint8List.sublistView(_png, offset)),
          'size': _png.length,
        };
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

/// 取缩略图一律失败的连接（断掉的那条）。
class _DeadThumbRpc extends _MediaRpc {
  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method == 'session.mediaThumb') {
      calls.add(method);
      throw RpcException('closed', 'connection closed');
    }
    return super.call(method, params, timeout: timeout);
  }
}

/// 连接可以换：模拟断线重连。
class _SwitchModel extends PosseAppModel {
  _SwitchModel(this.fakeRpc)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  PhoneRpc? fakeRpc;

  @override
  PhoneRpc? get rpc => fakeRpc;

  void swap(PhoneRpc next) {
    fakeRpc = next;
    notifyListeners();
  }
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _MediaRpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

void main() {
  const tab = SessionTab(
    worktreeId: 'w',
    title: 'claude',
    terminalId: 't1',
    agent: 'claude',
    sessionId: 'media-old',
  );

  test('only agent sessions with a session id have media', () {
    expect(hasMedia(tab), isTrue);
    expect(
      hasMedia(
        const SessionTab(worktreeId: 'w', title: 'sh', terminalId: 't2'),
      ),
      isFalse,
    );
  });

  testWidgets('lists made and pasted, opens the original image', (
    tester,
  ) async {
    final rpc = _MediaRpc();
    tester.view.physicalSize = const Size(800, 2400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final stamp = DateTime.now().microsecondsSinceEpoch;
    final t = SessionTab(
      worktreeId: 'w',
      title: 'claude',
      terminalId: 't1',
      agent: 'claude',
      sessionId: 'media-$stamp',
    );
    await tester.pumpWidget(
      MaterialApp(
        home: MediaScreen(model: _Model(rpc), tab: t),
      ),
    );
    await tester.pump();
    await tester.pump();
    expect(find.text('Agent 生成'), findsOneWidget);
    expect(find.text('41'), findsOneWidget);
    expect(find.text('只列出最近 2 项'), findsOneWidget);
    expect(find.text('粘贴的'), findsOneWidget);
    expect(find.text('rec.mp4'), findsOneWidget);
    expect(
      find.byIcon(Icons.play_arrow_rounded),
      findsOneWidget,
      reason: '视频盖播放标记',
    );
    expect(rpc.calls.where((m) => m == 'session.mediaThumb').length, 3);

    await tester.tap(find.text('phone_screenshot'));
    await tester.runAsync(() async {
      for (
        var i = 0;
        i < 20 && find.byType(ZoomImage).evaluate().isEmpty;
        i++
      ) {
        await Future<void>.delayed(const Duration(milliseconds: 20));
        await tester.pump();
      }
    });
    await tester.pumpAndSettle();
    expect(find.byType(ZoomImage), findsOneWidget);
    expect(rpc.reads, [0]);
    expect(find.textContaining('· 2/3'), findsOneWidget);

    // 左滑翻到粘贴的那张，顶栏跟着换
    await tester.fling(find.byType(ZoomImage), const Offset(-400, 0), 1500);
    await tester.runAsync(() async {
      for (var i = 0; i < 20 && find.text('#1').evaluate().isEmpty; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 20));
        await tester.pump();
      }
    });
    await tester.pumpAndSettle();
    expect(find.text('#1'), findsOneWidget);
    expect(find.textContaining('· 3/3'), findsOneWidget);
    expect(rpc.reads, [0, 0]);
    // 清掉这次测试下到临时目录的文件
    for (final f in Directory(
      '${Directory.systemTemp.path}/posse-video',
    ).listSync()) {
      if (f is File && f.lengthSync() == _png.length) f.deleteSync();
    }
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('thumbnails that failed load again after reconnecting', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(800, 2400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final stamp = DateTime.now().microsecondsSinceEpoch;
    final t = SessionTab(
      worktreeId: 'w',
      title: 'claude',
      terminalId: 't1',
      agent: 'claude',
      sessionId: 'media-retry-$stamp',
    );
    final dead = _DeadThumbRpc();
    final model = _SwitchModel(dead);
    await tester.pumpWidget(
      MaterialApp(
        home: MediaScreen(model: model, tab: t),
      ),
    );
    await tester.pump();
    await tester.pump();
    expect(find.byIcon(Icons.broken_image_rounded), findsNWidgets(2));

    final live = _MediaRpc();
    model.swap(live);
    await tester.pump();
    await tester.pump();
    expect(live.calls.where((m) => m == 'session.mediaThumb').length, 3);
    expect(find.byIcon(Icons.broken_image_rounded), findsNothing);
    await tester.pumpWidget(const SizedBox());
  });

  // 沿用 Orca 协议的旧版本回 unknown_method，同样算旧
  for (final code in ['method_not_found', 'unknown_method']) {
    testWidgets('old desktop says it needs an update ($code)', (tester) async {
      final rpc = _OldRpc(code);
      await tester.pumpWidget(
        MaterialApp(
          home: MediaScreen(model: _OldModel(rpc), tab: tab),
        ),
      );
      await tester.pump();
      await tester.pump();
      expect(find.textContaining('版本太旧'), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    });
  }
}

class _OldRpc implements PhoneRpc {
  _OldRpc(this.code);

  final String code;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async => throw RpcException(code, 'Unknown method: $method');

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _OldModel extends PosseAppModel {
  _OldModel(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _OldRpc fake;

  @override
  PhoneRpc? get rpc => fake;
}
