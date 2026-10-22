import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/files/files.dart';
import 'package:hive_mobile/protocol/rpc.dart';
import 'package:hive_mobile/ui/media_pager.dart';

void main() {
  _mediaPagerTests();
  test('directories come first and paths join the parent', () {
    final rows = parseDir('lib', [
      {'name': 'main.dart', 'isDirectory': false},
      {'name': 'ui', 'isDirectory': true},
      {'name': 'App.dart', 'isDirectory': false},
      {'name': ''},
      'junk',
    ]);
    expect(rows.map((e) => e.path), [
      'lib/ui',
      'lib/App.dart',
      'lib/main.dart',
    ]);
    expect(rows.first.dir, isTrue);
    expect(
      parseDir('', [
        {'name': 'a', 'isDirectory': false},
      ]).single.path,
      'a',
    );
    expect(parseDir('', null), isEmpty);
  });
}

// 1×1 的 PNG
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
);

class _FilesRpc implements PhoneRpc {
  final previews = <String>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    switch (method) {
      case 'files.readDir':
        return {
          'value': [
            {'name': 'notes.md', 'isDirectory': false},
            {'name': 'a.png', 'isDirectory': false},
            {'name': 'b.jpg', 'isDirectory': false},
            {'name': 'src', 'isDirectory': true},
          ],
        };
      case 'files.readPreview':
        previews.add(params['relativePath'] as String);
        return {'isImage': true, 'content': base64Encode(_png)};
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends HiveAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _FilesRpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

void _mediaPagerTests() {
  testWidgets('an image opened from the tree swipes to the next image', (
    tester,
  ) async {
    final rpc = _FilesRpc();
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: FilesPane(model: _Model(rpc), worktree: 'w'),
        ),
      ),
    );
    await tester.pump();
    await tester.pump();
    await tester.tap(find.text('a.png'));
    await tester.pumpAndSettle();
    expect(find.byType(ZoomImage), findsOneWidget);
    expect(rpc.previews, ['a.png']);

    await tester.fling(find.byType(ZoomImage), const Offset(-400, 0), 1500);
    await tester.pumpAndSettle();
    expect(rpc.previews, ['a.png', 'b.jpg']);
    expect(find.text('b.jpg'), findsOneWidget, reason: '顶栏标题跟着换');

    // 到头了
    await tester.fling(find.byType(ZoomImage), const Offset(-400, 0), 1500);
    await tester.pumpAndSettle();
    expect(rpc.previews, ['a.png', 'b.jpg']);
    await tester.pumpWidget(const SizedBox());
  });
}
