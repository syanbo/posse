import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/host/host_details_screen.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';

class _HostFakeRpc implements PhoneRpc {
  _HostFakeRpc(this.handler);

  final Future<Map<String, Object?>> Function(
    String method,
    Map<String, Object?> params,
  )
  handler;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) => handler(method, params);

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _HostFakeModel extends PosseAppModel {
  _HostFakeModel({PhoneRpc? rpc, String? computerId})
    : _fakeRpc = rpc,
      _fakeActiveId = computerId,
      super(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );

  PhoneRpc? _fakeRpc;
  String? _fakeActiveId;

  @override
  PhoneRpc? get rpc => _fakeRpc;

  @override
  String? get activeId => _fakeActiveId;

  void swapTransport(PhoneRpc? nextRpc) {
    _fakeRpc = nextRpc;
    notifyListeners();
  }

  void switchComputer(String? nextId, PhoneRpc? nextRpc) {
    _fakeActiveId = nextId;
    _fakeRpc = nextRpc;
    notifyListeners();
  }
}

Map<String, Object?> _hostInfoPayload({required bool enabled}) => {
  'version': '0.1.55',
  'commit': '6e22247',
  'build': '6e22247',
  'platform': 'linux',
  'arch': 'amd64',
  'edition': 'headless',
  'managed': true,
  'pid': 4242,
  'home': '/home/dev/.posse',
  'terminals': 1,
  'runningTerminals': 1,
  'daemonMemoryKB': 16384,
  'autostart': {
    'supported': true,
    'kind': 'systemd',
    'managed': true,
    'writable': true,
    'installed': enabled,
    'enabled': enabled,
    'active': enabled,
    'unit': 'posse-daemon.service',
  },
};

void main() {
  test('HostDetailsData and HostMemoryData parse JSON accurately', () {
    final info = HostDetailsData.fromJson({
      'version': '0.1.55',
      'commit': '6e22247abcdef',
      'build': '6e22247',
      'platform': 'linux',
      'arch': 'amd64',
      'edition': 'headless',
      'managed': true,
      'pid': 4242,
      'startedAt': 1700000000000,
      'home': '/home/dev/.posse',
      'terminals': 3,
      'runningTerminals': 2,
      'daemonMemoryKB': 45056,
      'autostart': {
        'supported': true,
        'kind': 'systemd',
        'managed': true,
        'writable': true,
        'installed': true,
        'enabled': true,
        'active': true,
        'unit': 'posse-daemon.service',
        'path': '/home/dev/.config/systemd/user/posse-daemon.service',
        'linger': 'no',
        'noteCode': 'systemd_linger_disabled',
      },
    });
    expect(info.platform, 'linux');
    expect(info.edition, 'headless');
    expect(info.managed, isTrue);
    expect(info.autostart?.kind, 'systemd');
    expect(info.autostart?.writable, isTrue);
    expect(info.autostart?.linger, 'no');

    final mem = HostMemoryData.fromJson({
      'totalKB': 131072,
      'truncated': true,
      'workspaces': [
        {
          'id': 'w1',
          'label': 'posse/main',
          'kb': 131072,
          'terminals': [
            {
              'id': 't1',
              'title': 'codex',
              'agent': 'codex',
              'kb': 131072,
              'procs': [
                {'name': 'node', 'kb': 102400, 'count': 3},
                {'name': 'bash', 'kb': 2048, 'count': 1},
              ],
            },
          ],
        },
      ],
    });
    expect(mem.totalKB, 131072);
    expect(mem.truncated, isTrue);
    final procs = mem.workspaces.single.terminals.single.procs;
    expect(procs.first.name, 'node');
    expect(procs.first.count, 3);
    expect(procs.last.name, 'bash');
    expect(procs.last.count, 1);
  });

  testWidgets('HostDetailsScreen renders disconnected fallback safely', (
    tester,
  ) async {
    final model = PosseAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(
      MaterialApp(
        locale: const Locale('zh'),
        theme: posseTheme(Brightness.light),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        home: HostDetailsScreen(model: model),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('主机详情'), findsOneWidget);
    expect(find.textContaining('连接主机后可查看'), findsOneWidget);
  });

  testWidgets(
    'same-computer transport swap reconciles after late write finishes',
    (tester) async {
      var serverEnabled = true;
      final writeCompleter = Completer<Map<String, Object?>>();

      final wsRpc = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          return _hostInfoPayload(enabled: serverEnabled);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        if (method == 'posse.host.autostartSet') {
          return writeCompleter.future;
        }
        return {};
      });

      var quicInfoReads = 0;
      final quicRpc = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          quicInfoReads++;
          return _hostInfoPayload(enabled: serverEnabled);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        return {};
      });

      final model = _HostFakeModel(rpc: wsRpc, computerId: 'comp-a');
      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          theme: posseTheme(Brightness.light),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          home: HostDetailsScreen(model: model),
        ),
      );
      await tester.pumpAndSettle();

      // 初始为开启
      Switch sw() => tester.widget<Switch>(find.byType(Switch));
      expect(sw().value, isTrue);
      expect(sw().onChanged, isNotNull);

      // 在 WS 上触发关闭写入（保持在途）
      await tester.tap(find.byType(Switch));
      await tester.pump();
      expect(sw().onChanged, isNull);

      // 同机切换传输 WS -> QUIC；QUIC 首次读先返回仍为开启（写还没落盘）
      model.swapTransport(quicRpc);
      await tester.pump();
      await tester.pump();
      expect(quicInfoReads, 1);
      // 旧写未结束前开关必须保持锁定，防止重复提交
      expect(sw().onChanged, isNull);

      // 随后旧 WS 写操作落盘成功，把服务端改为关闭
      serverEnabled = false;
      writeCompleter.complete(
        (_hostInfoPayload(enabled: false)['autostart'] as Map<String, Object?>),
      );
      await tester.pumpAndSettle();

      // 应自动在 QUIC 新连接上重拉对账，显示真实状态 false 并解锁开关
      expect(quicInfoReads, 2);
      expect(sw().value, isFalse);
      expect(sw().onChanged, isNotNull);
    },
  );

  testWidgets(
    'failed autostart write with failed reconcile keeps switch locked until refresh',
    (tester) async {
      var failInfo = false;
      final rpc = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          if (failInfo) throw Exception('network lost');
          return _hostInfoPayload(enabled: true);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        if (method == 'posse.host.autostartSet') {
          failInfo = true;
          throw Exception('timeout');
        }
        return {};
      });

      final model = _HostFakeModel(rpc: rpc, computerId: 'comp-a');
      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          theme: posseTheme(Brightness.light),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          home: HostDetailsScreen(model: model),
        ),
      );
      await tester.pumpAndSettle();

      Switch sw() => tester.widget<Switch>(find.byType(Switch));
      expect(sw().onChanged, isNotNull);

      // 写失败且随后的对账读也失败 -> 开关应保持禁用，不拿未知终态误解锁
      await tester.tap(find.byType(Switch));
      await tester.pumpAndSettle();
      expect(sw().onChanged, isNull);

      // 网络恢复后点刷新 -> 成功对账后重新解锁开关
      failInfo = false;
      await tester.tap(find.byIcon(Icons.refresh_rounded));
      await tester.pumpAndSettle();
      expect(sw().onChanged, isNotNull);
    },
  );

  testWidgets(
    'switching A -> B -> A isolates stale write reply from first visit to A',
    (tester) async {
      final oldWriteA = Completer<Map<String, Object?>>();
      final newWriteA = Completer<Map<String, Object?>>();
      var writeCallsA = 0;

      final rpcA1 = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          return _hostInfoPayload(enabled: true);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        if (method == 'posse.host.autostartSet') {
          writeCallsA++;
          return oldWriteA.future;
        }
        return {};
      });
      final rpcB = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          return _hostInfoPayload(enabled: false);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        return {};
      });
      final rpcA2 = _HostFakeRpc((method, params) async {
        if (method == 'posse.host.info') {
          return _hostInfoPayload(enabled: true);
        }
        if (method == 'posse.host.memory') {
          return {'totalKB': 0, 'workspaces': []};
        }
        if (method == 'posse.host.autostartSet') {
          writeCallsA++;
          return newWriteA.future;
        }
        return {};
      });

      final model = _HostFakeModel(rpc: rpcA1, computerId: 'comp-a');
      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          theme: posseTheme(Brightness.light),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          home: HostDetailsScreen(model: model),
        ),
      );
      await tester.pumpAndSettle();

      Switch sw() => tester.widget<Switch>(find.byType(Switch));

      // 第 1 次在 A 发起写入（挂起）
      await tester.tap(find.byType(Switch));
      await tester.pump();
      expect(writeCallsA, 1);

      // 切到 B，再切回 A（新代次）
      model.switchComputer('comp-b', rpcB);
      await tester.pumpAndSettle();
      model.switchComputer('comp-a', rpcA2);
      await tester.pumpAndSettle();

      // 第 2 次在 A 发起新写入（挂起）
      await tester.tap(find.byType(Switch));
      await tester.pump();
      expect(writeCallsA, 2);
      expect(sw().onChanged, isNull);

      // 此时第 1 次旧 A 写入迟到返回 -> 绝不能消耗第 2 次写入的锁定状态
      oldWriteA.complete(
        (_hostInfoPayload(enabled: false)['autostart'] as Map<String, Object?>),
      );
      await tester.pump();
      await tester.pump();
      expect(sw().onChanged, isNull);

      // 第 2 次新 A 写入真正完成后才解锁
      newWriteA.complete(
        (_hostInfoPayload(enabled: false)['autostart'] as Map<String, Object?>),
      );
      await tester.pumpAndSettle();
      expect(sw().value, isFalse);
      expect(sw().onChanged, isNotNull);
    },
  );

  testWidgets('a late reconcile cannot overwrite a more recent write', (
    tester,
  ) async {
    var infoCall = 0;
    var writeCall = 0;
    final staleReconcile = Completer<Map<String, Object?>>();

    final rpc = _HostFakeRpc((method, params) async {
      if (method == 'posse.host.info') {
        infoCall++;
        // 第 1 次初始读返回开启；第 2 次自动对账挂起；第 3 次手动刷新返回开启。
        if (infoCall == 2) return staleReconcile.future;
        return _hostInfoPayload(enabled: true);
      }
      if (method == 'posse.host.memory') {
        return {'totalKB': 0, 'workspaces': []};
      }
      if (method == 'posse.host.autostartSet') {
        writeCall++;
        if (writeCall == 1) throw Exception('first write failed');
        return (_hostInfoPayload(enabled: false)['autostart']
            as Map<String, Object?>);
      }
      return {};
    });

    final model = _HostFakeModel(rpc: rpc, computerId: 'comp-a');
    await tester.pumpWidget(
      MaterialApp(
        locale: const Locale('zh'),
        theme: posseTheme(Brightness.light),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        home: HostDetailsScreen(model: model),
      ),
    );
    await tester.pumpAndSettle();

    Switch sw() => tester.widget<Switch>(find.byType(Switch));

    // 第 1 次写失败 -> 触发自动对账（挂起）
    await tester.tap(find.byType(Switch));
    await tester.pump();
    await tester.pump();
    expect(sw().onChanged, isNull);

    // 手动刷新成功并解锁
    await tester.tap(find.byIcon(Icons.refresh_rounded));
    await tester.pumpAndSettle();
    expect(sw().onChanged, isNotNull);

    // 第 2 次写成功关闭自启
    await tester.tap(find.byType(Switch));
    await tester.pumpAndSettle();
    expect(sw().value, isFalse);

    // 第 1 次失败写留下的旧对账此刻迟到返回开启 -> 绝不能覆盖第 2 次成功写入
    staleReconcile.complete(_hostInfoPayload(enabled: true));
    await tester.pumpAndSettle();
    expect(sw().value, isFalse);
  });
}
