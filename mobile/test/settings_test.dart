import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/settings/settings.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  test('settings default to dark and terminal, and ignore bad values', () {
    final d = AppSettings.decode(null);
    expect(d.theme, 'dark');
    expect(d.openMode, 'terminal');
    final bad = AppSettings.decode(
      jsonEncode({'theme': 'pink', 'openMode': 1, 'terminalFontSize': 99}),
    );
    expect(bad.theme, 'dark');
    expect(bad.openMode, 'terminal');
    expect(bad.terminalFontSize, 18);
    expect(AppSettings.decode('not json').theme, 'dark');
  });

  test('updating a setting saves it', () async {
    final store = MemorySettingsStore();
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: store,
    );
    model.updateSettings((s) {
      s.theme = 'light';
      s.openMode = 'chat';
      s.showReasoning = false;
    });
    await Future<void>.delayed(Duration.zero);
    final back = AppSettings.decode(store.data);
    expect(back.theme, 'light');
    expect(back.openMode, 'chat');
    expect(back.showReasoning, isFalse);
  });

  test(
    'desk watch and the trackpad are on by default, once for old settings',
    () async {
      expect(AppSettings().deskWatch, isTrue);
      expect(AppSettings().deskTrackpad, isTrue);
      expect(
        AppSettings.decode(
          jsonEncode({'deskVersion': deskVersion, 'deskWatch': 'yes'}),
        ).deskWatch,
        isTrue,
      );
      // 实验时存下的是旧默认值：换成新的，换了操作方式再提示一次手势
      final old = AppSettings.decode(
        jsonEncode({
          'deskWatch': false,
          'deskTrackpad': false,
          'deskGesturesSeen': true,
        }),
      );
      expect(old.deskWatch, isTrue);
      expect(old.deskTrackpad, isTrue);
      expect(old.deskGesturesSeen, isFalse);
      // 新版之后自己关掉的照旧
      final chosen = AppSettings.decode(
        jsonEncode({
          'deskVersion': deskVersion,
          'deskWatch': false,
          'deskTrackpad': false,
          'deskGesturesSeen': true,
        }),
      );
      expect(chosen.deskWatch, isFalse);
      expect(chosen.deskTrackpad, isFalse);
      expect(chosen.deskGesturesSeen, isTrue);

      final store = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      model.updateSettings((s) => s.deskWatch = false);
      await Future<void>.delayed(Duration.zero);
      expect(AppSettings.decode(store.data).deskWatch, isFalse);
    },
  );

  testWidgets(
    'SettingsScreen mounts profile immediately, survives scrolling, and reuses cache',
    (tester) async {
      final rpc = _ProfileRpc();
      final model = _ProfileModel(rpc);
      addTearDown(model.dispose);

      Widget wrap() => MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: SettingsScreen(model: model, onComputers: () {}),
      );

      await tester.pumpWidget(wrap());
      // 首帧即挂载 ProfileSection 并发起 profile.get，不等滑到该区域
      expect(rpc.profileCalls, 1);

      rpc.complete({'name': 'Alice', 'avatar': ''});
      await tester.pump();
      expect(find.text('Alice'), findsOneWidget);

      // 滑到底部再滑回顶部，不会回收重建 ProfileSection，也不重复请求
      final scrollable = find.byType(SingleChildScrollView);
      await tester.drag(scrollable, const Offset(0, -1800));
      await tester.pumpAndSettle();
      await tester.drag(scrollable, const Offset(0, 1800));
      await tester.pumpAndSettle();
      expect(rpc.profileCalls, 1);
      expect(find.text('Alice'), findsOneWidget);

      // 同一连接再次进入设置页：即使刷新请求未返回，首帧也直接渲染缓存结果
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpWidget(wrap());
      expect(rpc.profileCalls, 2);
      expect(find.text('Alice'), findsOneWidget);

      rpc.complete({'name': 'Alice', 'avatar': ''});
      await tester.pump();
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'cold profile arriving after scroll down preserves visible row position',
    (tester) async {
      addTearDown(tester.view.reset);
      tester.view.physicalSize = const Size(400, 480);
      tester.view.devicePixelRatio = 1.0;

      final rpc = _ProfileRpc();
      final model = _ProfileModel(rpc);
      addTearDown(model.dispose);

      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: SettingsScreen(model: model, onComputers: () {}),
        ),
      );
      expect(rpc.profileCalls, 1);

      // 滚到底部看「关于」里的 Posse 行，再让 profile.get 晚到。
      final scrollable = find.byType(SingleChildScrollView);
      await tester.drag(scrollable, const Offset(0, -1800));
      await tester.pumpAndSettle();
      final beforeY = tester.getTopLeft(find.text('Posse')).dy;

      rpc.complete({'name': 'Bob', 'avatar': ''});
      await tester.pump();
      await tester.pump();
      final afterY = tester.getTopLeft(find.text('Posse')).dy;
      expect((afterY - beforeY).abs(), lessThan(1.0));
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'stale profile response from closed screen cannot overwrite newer cache and transient error keeps cache',
    (tester) async {
      final rpc = _ProfileRpc();
      final model = _ProfileModel(rpc);
      addTearDown(model.dispose);

      Widget wrap() => MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: SettingsScreen(model: model, onComputers: () {}),
      );

      // 第一次进入发请求 A（先不返回），关掉再重进发请求 B
      await tester.pumpWidget(wrap());
      final reqA = rpc.takePending();
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpWidget(wrap());
      final reqB = rpc.takePending();

      // B 先返回新名字，随后 A 迟到返回旧名字：缓存必须仍是 B 的新名字
      reqB.complete({'name': 'NewName', 'avatar': ''});
      await tester.pump();
      expect(find.text('NewName'), findsOneWidget);

      reqA.complete({'name': 'OldName', 'avatar': ''});
      await tester.pump();
      expect(find.text('NewName'), findsOneWidget);

      // 第三次进入：首帧展示 NewName，且刷新遇到瞬时超时仍保留 NewName
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpWidget(wrap());
      expect(find.text('NewName'), findsOneWidget);
      rpc.takePending().completeError(TimeoutException('slow'));
      await tester.pump();
      expect(find.text('NewName'), findsOneWidget);

      await tester.pumpWidget(const SizedBox.shrink());
    },
  );
}

class _ProfileRpc implements PhoneRpc {
  int profileCalls = 0;
  final _queue = <Completer<Map<String, Object?>>>[];

  Completer<Map<String, Object?>> takePending() => _queue.removeAt(0);

  void complete(Map<String, Object?> value) {
    if (_queue.isNotEmpty) _queue.removeLast().complete(value);
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) {
    if (method == 'discussion.profile.get') {
      profileCalls++;
      final c = Completer<Map<String, Object?>>();
      _queue.add(c);
      return c.future;
    }
    return Future.value({});
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _ProfileModel extends PosseAppModel {
  _ProfileModel(this._rpc)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _ProfileRpc _rpc;

  @override
  String? get activeId => 'c1';

  @override
  PhoneRpc? get rpc => _rpc;
}
