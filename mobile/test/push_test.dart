import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:flutter/material.dart';
import 'package:posse_mobile/settings/settings.dart';
import 'package:posse_mobile/main.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/ui/widgets.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/push/push.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/workspace/models.dart';

void main() {
  // main.dart 在 runApp 之前建模型，那时还没有 binding：这里在 setUp 初始化 binding 之前建
  Object? early;
  try {
    ChannelPush();
  } catch (e) {
    early = e;
  }
  test('ChannelPush can be built before the binding exists', () {
    expect(early, isNull);
  });

  test('push switches default on and round trip', () {
    final s = AppSettings();
    expect(s.pushWaiting && s.pushDone, isTrue);
    expect(s.pushAsked, isFalse);
    s
      ..pushWaiting = false
      ..pushAsked = true;
    final back = AppSettings.decode(jsonEncode(s.toJson()));
    expect(back.pushWaiting, isFalse);
    expect(back.pushDone, isTrue);
    expect(back.pushAsked, isTrue);
  });

  test('no push platform in tests means unsupported', () {
    final model = PosseAppModel(store: MemoryPairingStore());
    // 测试环境不是 iOS，不碰原生通道
    expect(model.push, isNull);
    expect(model.pushStatus, PushStatus.unsupported);
  });
  test('legacy disabled types stay disabled after migration', () {
    final s = AppSettings.decode('{"pushWaiting":false,"pushDone":false}');
    expect(s.pushEnabled, isFalse);
    expect(s.badgeEnabled, isFalse);
    expect(AppSettings.decode('{}').pushEnabled, isTrue);
    expect(AppSettings.decode('{}').badgeEnabled, isTrue);
  });

  for (final alerts in [false, true]) {
    for (final badge in [false, true]) {
      test(
        'independent preferences persist: alerts=$alerts badge=$badge',
        () async {
          final p = _Push();
          final r = _Rpc();
          final store = MemorySettingsStore();
          final m = _Model(p, r, store);
          m.updateSettings((s) {
            s.pushEnabled = alerts;
            s.badgeEnabled = badge;
            s.pushWaiting = false;
          });
          await m.registerPush();
          expect(p.badges.last, badge ? 2 : 0);
          expect(r.calls.last.$2['token'], alerts || badge ? 'abcd' : '');
          if (alerts || badge) {
            expect(r.calls.last.$2['alerts'], alerts);
            expect(r.calls.last.$2['badge'], badge);
            expect(r.calls.last.$2['waiting'], isFalse);
          }
          final restarted = _Model(_Push(), _Rpc(), store);
          await restarted.restore();
          expect(restarted.settings.pushEnabled, alerts);
          expect(restarted.settings.badgeEnabled, badge);
          expect(restarted.settings.pushWaiting, isFalse);
          await restarted.registerPush();
          expect(
            restarted.fake.calls.last.$2['token'],
            alerts || badge ? 'abcd' : '',
          );
          m.dispose();
          restarted.dispose();
        },
      );
    }
  }

  test('turning badge off clears it while disconnected', () async {
    final p = _Push();
    final m = _Model(p, _Rpc(), MemorySettingsStore());
    await m.registerPush();
    expect(p.badges.last, 2);
    m.connected = false;
    m.updateSettings((s) => s.badgeEnabled = false);
    await m.registerPush();
    expect(p.badges.last, 0);
    expect(m.settings.pushEnabled, isTrue);
    m.dispose();
  });

  test(
    'denied alerts still register allowed badges and show permission guidance',
    () async {
      final p = _Push()
        ..auth = const PushAuthorization('authorized', badge: true);
      final r = _Rpc();
      final m = _Model(p, r, MemorySettingsStore());
      await m.registerPush();
      expect(m.pushStatus, PushStatus.denied);
      expect(r.calls.last.$2['alerts'], isFalse);
      expect(r.calls.last.$2['badge'], isTrue);
      expect(p.badges.last, 2);
      p.auth = const PushAuthorization('denied');
      await m.registerPush();
      expect(r.calls.last.$2, {'token': ''});
      expect(p.badges.last, 0);
      expect(p.requests, 0);
      m.dispose();
    },
  );

  test(
    'permission requested once and refreshed after returning from settings',
    () async {
      final p = _Push()..auth = const PushAuthorization('notDetermined');
      final m = _Model(p, _Rpc(), MemorySettingsStore());
      await m.registerPush();
      await m.registerPush();
      expect(p.requests, 1);
      expect(m.pushStatus, PushStatus.denied);
      p.auth = const PushAuthorization('authorized', alerts: true, badge: true);
      await m.registerPush();
      expect(m.pushStatus, PushStatus.on);
      m.dispose();
    },
  );

  test(
    'old computer retains defaults but unregisters independent choices',
    () async {
      final r = _Rpc()..old = true;
      final m = _Model(_Push(), r, MemorySettingsStore());
      await m.registerPush();
      expect(m.pushStatus, PushStatus.on);
      expect(r.calls.last.$1, 'posse.push.register');
      expect(r.calls.last.$2['token'], 'abcd');
      m.settings.pushEnabled = false;
      await m.registerPush();
      expect(m.pushStatus, PushStatus.unsupportedComputer);
      expect(r.calls.last.$2, {'token': ''});
      m.dispose();
    },
  );

  for (final kind in ['waiting', 'done']) {
    test(
      'old computer cannot preserve badges for disabled $kind notifications',
      () async {
        final r = _Rpc()..old = true;
        final m = _Model(_Push(), r, MemorySettingsStore());
        if (kind == 'waiting') {
          m.settings.pushWaiting = false;
        } else {
          m.settings.pushDone = false;
        }
        await m.registerPush();
        expect(m.pushStatus, PushStatus.unsupportedComputer);
        expect(r.calls.last.$1, 'posse.push.register');
        expect(r.calls.last.$2, {'token': ''});
        m.dispose();
      },
    );
  }

  testWidgets(
    'each background return refreshes permissions and badge without wake registration loops',
    (tester) async {
      final p = _Push();
      final r = _Rpc();
      final m = _LifecycleModel(p, r, MemorySettingsStore());
      await m.registerPush();
      expect(m.pushStatus, PushStatus.on);
      await tester.pumpWidget(PosseApp(model: m));
      await tester.pumpAndSettle();
      int registrations() =>
          r.calls.where((call) => call.$1 == 'posse.push.configure').length;
      final initial = registrations();
      Future<void> resume() async {
        tester.binding.handleAppLifecycleStateChanged(AppLifecycleState.paused);
        tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.resumed,
        );
        await tester.pumpAndSettle();
      }

      p.auth = const PushAuthorization('authorized', alerts: true);
      await resume();
      expect(m.pushStatus, PushStatus.denied);
      expect(p.systemBadge, 0);
      expect(
        r.calls
            .lastWhere((call) => call.$1 == 'posse.push.configure')
            .$2['badge'],
        isFalse,
      );
      expect(registrations(), initial + 1);
      p.auth = const PushAuthorization('authorized', alerts: true, badge: true);
      await resume();
      expect(m.pushStatus, PushStatus.on);
      expect(p.systemBadge, 2);
      expect(registrations(), initial + 2);
      // 后台另一台电脑写过角标，当前会话计数未变，回前台仍需重写系统值。
      p.systemBadge = 5;
      await resume();
      expect(p.systemBadge, 2);
      expect(registrations(), initial + 3);
      for (var i = 0; i < 3; i++) {
        await m.wake();
        tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.inactive,
        );
        tester.binding.handleAppLifecycleStateChanged(
          AppLifecycleState.resumed,
        );
      }
      await tester.pumpAndSettle();
      expect(registrations(), initial + 3);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets(
    'settings switches act independently and preserve notification types',
    (tester) async {
      final p = _Push();
      final m = _Model(p, _Rpc(), MemorySettingsStore());
      m.settings.pushWaiting = false;
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: SettingsScreen(model: m, onComputers: () {}),
        ),
      );
      await tester.scrollUntilVisible(find.text('角标'), 400);
      Finder toggle(String text) => find.descendant(
        of: find
            .ancestor(of: find.text(text).last, matching: find.byType(Row))
            .first,
        matching: find.byType(PosseSwitch),
      );
      await tester.tap(toggle('通知'));
      await tester.pumpAndSettle();
      expect(m.settings.pushEnabled, isFalse);
      expect(m.settings.badgeEnabled, isTrue);
      expect(find.text('agent 等你处理时'), findsNothing);
      await tester.tap(toggle('角标'));
      await tester.pumpAndSettle();
      expect(m.settings.badgeEnabled, isFalse);
      expect(p.badges.last, 0);
      await tester.tap(toggle('通知'));
      await tester.pumpAndSettle();
      expect(m.settings.pushEnabled, isTrue);
      expect(m.settings.pushWaiting, isFalse);
      expect(m.settings.pushDone, isTrue);
      expect(m.settings.badgeEnabled, isFalse);
      await tester.pumpWidget(const SizedBox.shrink());
      m.dispose();
    },
  );

  test('old relay is explicitly shown', () async {
    final r = _Rpc()..badgeOnly = false;
    final m = _Model(_Push(), r, MemorySettingsStore());
    m.settings.pushEnabled = false;
    await m.registerPush();
    expect(m.pushStatus, PushStatus.unsupportedRelay);
    m.dispose();
  });
}

class _Push implements PushPlatform {
  var auth = const PushAuthorization('authorized', alerts: true, badge: true);
  final badges = <int>[];
  int requests = 0;
  int systemBadge = 0;
  @override
  Future<PushAuthorization> authorization() async => auth;
  @override
  Future<bool> request() async {
    requests++;
    auth = const PushAuthorization('denied');
    return false;
  }

  @override
  Future<String> token() async => 'abcd';
  @override
  Future<String> key() async => 'key';
  @override
  Future<bool> sandbox() async => false;
  @override
  Future<void> setLanguage(String tag) async {}
  @override
  Future<void> setBadge(int count) async {
    badges.add(count);
    systemBadge = count;
  }

  @override
  Future<void> configure({required bool alerts, required bool badge}) async {}
  @override
  Future<void> openSettings() async {}
  @override
  Future<Map<String, String>?> takeOpened() async => null;
  @override
  set onOpened(void Function()? callback) {}
}

class _Rpc implements PhoneRpc {
  final calls = <(String, Map<String, Object?>)>[];
  bool old = false;
  bool badgeOnly = true;
  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    calls.add((method, params));
    if (method == 'posse.push.configure' && old) {
      throw RpcException('method_not_found', 'missing');
    }
    return {'relay': true, 'badgeOnly': badgeOnly};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(PushPlatform push, this.fake, SettingsStore store)
    : super(push: push, store: MemoryPairingStore(), settingsStore: store) {
    tabs = {
      'w': [
        const SessionTab(
          worktreeId: 'w',
          title: 'a',
          terminalId: '1',
          posse: 'waiting',
        ),
        const SessionTab(
          worktreeId: 'w',
          title: 'b',
          terminalId: '2',
          posse: 'unread',
        ),
        const SessionTab(
          worktreeId: 'w',
          title: 'c',
          terminalId: '3',
          posse: 'working',
        ),
      ],
    };
  }
  final _Rpc fake;
  bool connected = true;
  @override
  PhoneRpc? get rpc => connected ? fake : null;
}

class _LifecycleModel extends _Model {
  _LifecycleModel(super.push, super.fake, super.store);
  @override
  Future<void> restore() async {
    restored = true;
  }

  @override
  Future<void> refreshWorktrees({bool poll = false}) async {}
}
