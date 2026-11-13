import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/files/files.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/media_pager.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  _treeStatusTests();
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
    // 符号链接画带箭头的图标；旧电脑不给 isSymlink 时当普通文件
    final links = parseDir('', [
      {'name': 'docs', 'isDirectory': true, 'isSymlink': true},
      {'name': 'a.go', 'isDirectory': false},
    ]);
    expect(links.map((e) => e.link), [true, false]);
  });
}

// 1×1 的 PNG
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
);

class _FilesRpc implements PhoneRpc {
  final previews = <String>[];
  final treeReads = <List<String>>[];
  final marks = <String, String>{};
  final ignored = <String>{};
  bool treeSupported = true;
  bool treeFails = false;
  bool extraFile = false;
  bool gitUnavailable = false;
  bool splitTree = false;
  final dirReads = <String>[];
  Completer<Map<String, Object?>>? pendingTree;
  Completer<Map<String, Object?>>? pendingDir;

  /// 读 src 时先失败几次。
  var failSrc = 0;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method == 'files.readDir') {
      dirReads.add(params['relativePath'] as String);
      if (pendingDir case final pending?) {
        pendingDir = null;
        return pending.future;
      }
    }
    switch (method) {
      case 'files.tree':
        final dirs = (params['directories'] as List).cast<String>();
        treeReads.add(dirs);
        if (!treeSupported) throw RpcException('method_not_found', 'old host');
        if (treeFails) throw RpcException('git', 'query failed');
        if (pendingTree case final pending?) {
          pendingTree = null;
          return pending.future;
        }
        final snapshots = <String, Object?>{};
        for (final dir in splitTree ? dirs.take(1) : dirs) {
          try {
            final result = await call('files.readDir', {'relativePath': dir});
            snapshots[dir] = {
              'entries': [
                for (final raw in result['value'] as List)
                  {
                    ...raw as Map,
                    'gitStatus':
                        marks[dir.isEmpty
                            ? raw['name']
                            : '$dir/${raw['name']}'] ??
                        '',
                    'ignored': ignored.contains(
                      dir.isEmpty ? raw['name'] : '$dir/${raw['name']}',
                    ),
                  },
              ],
            };
          } catch (_) {
            snapshots[dir] = {'error': 'read failed'};
          }
        }
        return {
          'directories': snapshots,
          if (gitUnavailable) 'gitError': 'Git status unavailable',
          if (splitTree && dirs.length > 1) 'remaining': dirs.skip(1).toList(),
        };
      case 'files.readDir' when params['relativePath'] == 'src':
        if (failSrc > 0) {
          failSrc--;
          throw RpcException('timeout', 'request timed out');
        }
        return {
          'value': [
            {'name': 'main.go', 'isDirectory': false},
          ],
        };
      case 'files.readDir':
        return {
          'value': [
            {'name': 'notes.md', 'isDirectory': false},
            {'name': 'a.png', 'isDirectory': false},
            {'name': 'b.jpg', 'isDirectory': false},
            {'name': 'src', 'isDirectory': true},
            if (extraFile) {'name': 'new.txt', 'isDirectory': false},
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

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  _FilesRpc fake;

  void reconnect(_FilesRpc rpc) {
    fake = rpc;
    notifyListeners();
  }

  @override
  PhoneRpc? get rpc => fake;
}

void _treeStatusTests() {
  for (final brightness in Brightness.values) {
    testWidgets(
      'file tree follows desktop status colors and ignored priority $brightness',
      (tester) async {
        final rpc = _FilesRpc()
          ..marks.addAll({
            'src': 'M',
            'src/main.go': 'A',
            'notes.md': '?',
            'a.png': 'U',
            'b.jpg': 'A',
          })
          ..ignored.add('b.jpg');
        final model = _Model(rpc);
        addTearDown(model.dispose);
        await tester.pumpWidget(
          MaterialApp(
            theme: posseTheme(brightness),
            home: Scaffold(
              body: FilesPane(model: model, worktree: 'w'),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final c = PosseColors.of(tester.element(find.text('notes.md')));
        Color? color(String name) =>
            tester.widget<Text>(find.text(name)).style?.color;
        expect(color('src'), c.sky);
        expect(color('notes.md'), c.gitUntracked);
        expect(color('a.png'), c.gitDanger);
        expect(color('b.jpg'), c.muted);
        final faded = find.ancestor(
          of: find.text('b.jpg'),
          matching: find.byType(Opacity),
        );
        expect(tester.widget<Opacity>(faded.first).opacity, 0.7);
        await tester.tap(find.text('src'));
        await tester.pumpAndSettle();
        expect(color('main.go'), c.sage);
        rpc.marks.clear();
        rpc.ignored.clear();
        rpc.extraFile = true;
        await tester.pump(const Duration(seconds: 3));
        await tester.pumpAndSettle();
        expect(rpc.treeReads.last, ['', 'src']);
        expect(color('main.go'), c.text);
        expect(color('notes.md'), c.text);
        expect(color('src'), c.text);
        expect(find.text('new.txt'), findsOneWidget);
        await tester.pumpWidget(const SizedBox());
      },
    );
  }

  testWidgets(
    'file tree pauses when hidden and keeps colors after failed refresh',
    (tester) async {
      final rpc = _FilesRpc()..marks['notes.md'] = '?';
      final model = _Model(rpc);
      addTearDown(model.dispose);
      Widget app(bool active) => MaterialApp(
        home: Scaffold(
          body: FilesPane(model: model, worktree: 'w', active: active),
        ),
      );
      await tester.pumpWidget(app(true));
      await tester.pumpAndSettle();
      final color = tester.widget<Text>(find.text('notes.md')).style?.color;
      final reads = rpc.treeReads.length;
      await tester.pumpWidget(app(false));
      await tester.pump(const Duration(seconds: 9));
      expect(rpc.treeReads.length, reads);
      rpc.treeFails = true;
      await tester.pumpWidget(app(true));
      await tester.pumpAndSettle();
      expect(rpc.treeReads.length, reads + 1);
      expect(tester.widget<Text>(find.text('notes.md')).style?.color, color);
      expect(find.text('query failed'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'Git failures keep browsing and old colors; split replies finish',
    (tester) async {
      final rpc = _FilesRpc()..marks['notes.md'] = '?';
      final model = _Model(rpc);
      addTearDown(model.dispose);
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(
            body: FilesPane(model: model, worktree: 'w'),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final color = tester.widget<Text>(find.text('notes.md')).style?.color;
      rpc
        ..gitUnavailable = true
        ..extraFile = true
        ..marks.clear();
      await tester.pump(const Duration(seconds: 3));
      await tester.pumpAndSettle();
      expect(find.text('new.txt'), findsOneWidget);
      expect(find.text('Git status unavailable'), findsOneWidget);
      expect(tester.widget<Text>(find.text('notes.md')).style?.color, color);
      await tester.tap(find.text('src'));
      await tester.pumpAndSettle();
      expect(find.text('main.go'), findsOneWidget);
      rpc
        ..gitUnavailable = false
        ..splitTree = true
        ..marks['src/main.go'] = 'A';
      await tester.pump(const Duration(seconds: 3));
      await tester.pumpAndSettle();
      expect(rpc.treeReads.sublist(rpc.treeReads.length - 2), [
        ['', 'src'],
        ['src'],
      ]);
      expect(find.text('Git status unavailable'), findsNothing);
      final c = PosseColors.of(tester.element(find.text('main.go')));
      expect(tester.widget<Text>(find.text('main.go')).style?.color, c.sage);
      expect(tester.widget<Text>(find.text('notes.md')).style?.color, c.text);
      await tester.pumpWidget(const SizedBox());
    },
  );

  for (final leave in ['hidden', 'disposed', 'reconnected', 'background']) {
    testWidgets('old host stops pending directory batch when $leave', (
      tester,
    ) async {
      final rpc = _FilesRpc()..treeSupported = false;
      final model = _Model(rpc);
      addTearDown(model.dispose);
      Widget app(bool active) => MaterialApp(
        home: Scaffold(
          body: FilesPane(model: model, worktree: 'w', active: active),
        ),
      );
      await tester.pumpWidget(app(true));
      await tester.pumpAndSettle();
      await tester.tap(find.text('src'));
      await tester.pumpAndSettle();
      final pending = Completer<Map<String, Object?>>();
      rpc.pendingDir = pending;
      final before = rpc.dirReads.length;
      await tester.pump(const Duration(seconds: 3));
      expect(rpc.dirReads.length, before + 1);
      switch (leave) {
        case 'hidden':
          await tester.pumpWidget(app(false));
        case 'disposed':
          await tester.pumpWidget(const SizedBox());
        case 'reconnected':
          model.reconnect(_FilesRpc());
          await tester.pumpAndSettle();
        case 'background':
          tester.binding.handleAppLifecycleStateChanged(
            AppLifecycleState.hidden,
          );
          addTearDown(
            () => tester.binding.handleAppLifecycleStateChanged(
              AppLifecycleState.resumed,
            ),
          );
      }
      pending.complete({'value': <Object>[]});
      await tester.pumpAndSettle();
      expect(rpc.dirReads.length, before + 1);
      await tester.pumpWidget(const SizedBox());
    });
  }

  testWidgets('old hosts keep browsing and reconnect ignores old snapshots', (
    tester,
  ) async {
    final old = _FilesRpc()..treeSupported = false;
    final model = _Model(old);
    addTearDown(model.dispose);
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: FilesPane(model: model, worktree: 'w'),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('notes.md'), findsOneWidget);
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();
    expect(old.treeReads.length, 1);
    final pending = Completer<Map<String, Object?>>();
    final slow = _FilesRpc()..pendingTree = pending;
    model.reconnect(slow);
    await tester.pump();
    final fresh = _FilesRpc()..marks['notes.md'] = 'A';
    model.reconnect(fresh);
    await tester.pumpAndSettle();
    pending.complete({
      'directories': {
        '': {'entries': []},
      },
    });
    await tester.pumpAndSettle();
    final c = PosseColors.of(tester.element(find.text('notes.md')));
    expect(tester.widget<Text>(find.text('notes.md')).style?.color, c.sage);
    await tester.pumpWidget(const SizedBox());
  });
}

void _mediaPagerTests() {
  testWidgets('a folder that fails to read says so and reads again on tap', (
    tester,
  ) async {
    final rpc = _FilesRpc()..failSrc = 1;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: FilesPane(model: _Model(rpc), worktree: 'w'),
        ),
      ),
    );
    await tester.pump();
    await tester.pump();
    await tester.tap(find.text('src'));
    await tester.pump();
    await tester.pump();
    expect(find.text('重试'), findsOneWidget);
    expect(find.text('main.go'), findsNothing);

    await tester.tap(find.text('重试'));
    await tester.pump();
    await tester.pump();
    expect(find.text('重试'), findsNothing);
    expect(find.text('main.go'), findsOneWidget);
    await tester.pumpWidget(const SizedBox());
  });

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
