import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:image_picker_platform_interface/image_picker_platform_interface.dart';
import 'package:plugin_platform_interface/plugin_platform_interface.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/chat.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/widgets.dart';
import 'package:posse_mobile/workspace/models.dart';

/// 相册：选中的就是这几个视频。
class _Picker extends ImagePickerPlatform with MockPlatformInterfaceMixin {
  _Picker(this.files);
  final List<XFile> files;

  @override
  Future<List<XFile>> getMedia({required MediaOptions options}) async => files;
}

/// 对话页用到的 RPC：订阅不推东西；上传走旧电脑的 base64 路径（一段传完就提交），
/// 第 [failAt] 个文件提交时失败。
class _Rpc implements PhoneRpc {
  _Rpc({required this.failAt});
  final int failAt;
  var started = 0;
  final commitGate = <int, Completer<void>>{};
  Completer<void>? saveGate;

  /// 照 PhoneRpc.upload：换下来之后不接新的上传
  var draining = false;
  void Function()? onCommitted;

  @override
  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async => RpcStream(method, const Stream.empty(), () async {});

  @override
  Future<T> upload<T>(Future<T> Function() body) {
    if (draining) throw UploadRefused();
    return body();
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    switch (method) {
      case 'clipboard.startImageUpload':
        started++;
        return {'uploadId': 'u$started'};
      case 'clipboard.saveImageAsTempFile':
        await saveGate?.future;
        return {'value': '/tmp/photo.png'};
      case 'terminal.send':
        return {
          'send': {
            'accepted': true,
            'writeSettlement': {'outcome': 'accepted'},
          },
        };
      case 'clipboard.commitImageUpload':
        final n = int.parse((params['uploadId'] as String).substring(1));
        await commitGate[n]?.future;
        if (n == failAt) throw RpcException('internal_error', 'disk full');
        onCommitted?.call();
        return {'value': '/tmp/up$n.mov'};
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  _Rpc fake;
  @override
  PhoneRpc? get rpc => fake;
}

const _tab = SessionTab(
  worktreeId: 'w',
  title: 'claude',
  terminalId: 't1',
  agent: 'claude',
  state: 'idle',
  sessionId: 's1',
);

// 1×1 的 PNG
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
);

XFile _video(int n) =>
    XFile.fromData(Uint8List.fromList([n, n, n]), path: 'clip$n.mov');

Future<void> _pickVideos(WidgetTester tester) async {
  await tester.tap(find.byTooltip('发送图片'));
  await tester.pumpAndSettle();
  await tester.tap(find.text('从相册选择照片或视频，可以多选'));
  await tester.pump();
}

void main() {
  late ImagePickerPlatform saved;
  setUp(() => saved = ImagePickerPlatform.instance);
  tearDown(() => ImagePickerPlatform.instance = saved);

  testWidgets('a failure on the third file keeps the paths already uploaded', (
    tester,
  ) async {
    ImagePickerPlatform.instance = _Picker([_video(1), _video(2), _video(3)]);
    final rpc = _Rpc(failAt: 3);
    await tester.pumpWidget(
      MaterialApp(
        home: ChatScreen(model: _Model(rpc), tab: _tab),
      ),
    );
    await tester.pump();
    await _pickVideos(tester);
    await tester.runAsync(() => Future<void>.delayed(Duration.zero));
    await tester.pumpAndSettle();
    final field = tester.widget<TextField>(find.byType(TextField));
    expect(field.controller!.text, contains('/tmp/up1.mov'));
    expect(field.controller!.text, contains('/tmp/up2.mov'));
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('finishing an attachment does not hide the progress of a send', (
    tester,
  ) async {
    final rpc = _Rpc(failAt: 0)
      ..commitGate[1] = Completer<void>()
      ..saveGate = Completer<void>();
    final picker = _Picker([_video(1)]);
    ImagePickerPlatform.instance = picker;
    await tester.pumpWidget(
      MaterialApp(
        home: ChatScreen(model: _Model(rpc), tab: _tab),
      ),
    );
    await tester.pump();
    // 附加一个视频，卡在提交上
    await _pickVideos(tester);
    await tester.pump();
    // 接着加一张图片发出去，卡在传图上
    picker.files
      ..clear()
      ..add(XFile.fromData(_png, path: 'p.png'));
    await _pickVideos(tester);
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('发送'));
    await tester.pump();
    expect(find.byType(UploadBar), findsOneWidget);
    // 附加的视频传完：发送的进度条还在
    rpc.commitGate[1]!.complete();
    await tester.pump();
    await tester.pump();
    expect(
      find.byType(UploadBar),
      findsOneWidget,
      reason: 'send progress hidden',
    );
    rpc.saveGate!.complete();
    await tester.pumpAndSettle();
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'a second attachment finishing first keeps the first one\'s progress',
    (tester) async {
      final rpc = _Rpc(failAt: 0)..commitGate[1] = Completer<void>();
      final picker = _Picker([_video(1)]);
      ImagePickerPlatform.instance = picker;
      await tester.pumpWidget(
        MaterialApp(
          home: ChatScreen(model: _Model(rpc), tab: _tab),
        ),
      );
      await tester.pump();
      await _pickVideos(tester);
      await tester.pump();
      picker.files
        ..clear()
        ..add(_video(2));
      await _pickVideos(tester);
      await tester.pumpAndSettle();
      expect(
        find.byType(UploadBar),
        findsOneWidget,
        reason: 'first attachment progress hidden',
      );
      rpc.commitGate[1]!.complete();
      await tester.pumpAndSettle();
      expect(find.byType(UploadBar), findsNothing);
      final field = tester.widget<TextField>(find.byType(TextField));
      expect(field.controller!.text, contains('/tmp/up1.mov'));
      expect(field.controller!.text, contains('/tmp/up2.mov'));
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'a multi-file selection moves to the new connection after a switch',
    (tester) async {
      final old = _Rpc(failAt: 0);
      final next = _Rpc(failAt: 0);
      final model = _Model(old);
      // 第一份传完时换了连接：旧的开始排空，model.rpc 换成新的
      old.onCommitted = () {
        old.draining = true;
        model.fake = next;
        model.notifyListeners();
      };
      ImagePickerPlatform.instance = _Picker([_video(1), _video(2)]);
      await tester.pumpWidget(
        MaterialApp(
          home: ChatScreen(model: model, tab: _tab),
        ),
      );
      await tester.pump();
      await _pickVideos(tester);
      await tester.runAsync(() => Future<void>.delayed(Duration.zero));
      await tester.pumpAndSettle();
      expect(
        next.started,
        1,
        reason: 'the second file stayed on the draining connection',
      );
      final field = tester.widget<TextField>(find.byType(TextField));
      expect(field.controller!.text, contains('/tmp/up1.mov'));
      await tester.pumpWidget(const SizedBox());
    },
  );
}
