import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/files/files.dart';
import 'package:posse_mobile/protocol/rpc.dart';

class _FakeRpc implements PhoneRpc {
  _FakeRpc(this.content);

  final String content;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async => {'content': content};

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _FakeModel extends PosseAppModel {
  _FakeModel(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _FakeRpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

void main() {
  testWidgets('源码按行只建看得见的，复制时行之间有换行', (tester) async {
    String? copied;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied = (call.arguments as Map)['text'] as String?;
        }
        return null;
      },
    );
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        null,
      ),
    );
    final text = [for (var i = 0; i < 2000; i++) 'line $i'].join('\n');
    await tester.pumpWidget(
      MaterialApp(
        home: FileViewer(
          model: _FakeModel(_FakeRpc('alpha\n\ngamma')),
          worktree: 'w',
          path: 'a.txt',
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.textContaining('alpha', findRichText: true));
    await tester.sendKeyDownEvent(LogicalKeyboardKey.control);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.control);
    await tester.pump();
    expect(copied, 'alpha\n\ngamma');

    await tester.pumpWidget(
      MaterialApp(
        home: FileViewer(
          key: const ValueKey('big'),
          model: _FakeModel(_FakeRpc(text)),
          worktree: 'w',
          path: 'b.txt',
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('line 0', findRichText: true), findsOneWidget);
    expect(find.textContaining('line 1999', findRichText: true), findsNothing);
  });
}
