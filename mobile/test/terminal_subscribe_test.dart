import 'dart:async';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/terminal/terminal.dart';
import 'package:posse_mobile/ui/widgets.dart';
import 'package:posse_mobile/workspace/models.dart';

/// 订阅被常驻进程拒掉：错误回复在订阅返回之后才到，作为流里的错误送来。
class _RefusingRpc implements PhoneRpc {
  final binaryCtl = StreamController<Uint8List>.broadcast();

  @override
  Stream<Uint8List> get binary => binaryCtl.stream;

  @override
  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async {
    final frames = StreamController<Map<String, Object?>>();
    scheduleMicrotask(() {
      frames.addError(RpcException('not_found', 'terminal t1 not found'));
    });
    return RpcStream('rpc-1', frames.stream, () async {
      await frames.close();
    });
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async => {};

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _RefusingRpc fake;

  @override
  PhoneRpc? get rpc => fake;

  void bind(SessionTab tab) {
    tabs = {
      tab.worktreeId: [tab],
    };
    notifyListeners();
  }
}

void main() {
  testWidgets('a terminal can open chat after Antigravity binds a session', (
    tester,
  ) async {
    final model = _Model(_RefusingRpc());
    SessionTab? opened;
    const before = SessionTab(
      worktreeId: 'w',
      title: 'Antigravity',
      terminalId: 't1',
      agent: 'antigravity',
    );
    const after = SessionTab(
      worktreeId: 'w',
      title: 'Antigravity',
      terminalId: 't1',
      agent: 'antigravity',
      sessionId: 'current-session',
    );
    await tester.pumpWidget(
      MaterialApp(
        home: TerminalScreen(
          model: model,
          tab: before,
          onOpenChat: (tab) => opened = tab,
        ),
      ),
    );
    for (var i = 0; i < 5; i++) {
      await tester.pump(const Duration(milliseconds: 10));
    }
    model.bind(after);
    await tester.pump();
    tester
        .widget<IconButton>(
          find.widgetWithIcon(IconButton, Icons.more_horiz_rounded),
        )
        .onPressed!();
    await tester.pump();
    final switchRow = find.ancestor(
      of: find.byIcon(Icons.chat_bubble_outline_rounded),
      matching: find.byType(RowTile),
    );
    tester.widget<RowTile>(switchRow).onTap!();
    await tester.pump();
    expect(opened?.sessionId, 'current-session');
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('a refused subscription says so instead of spinning', (
    tester,
  ) async {
    const tab = SessionTab(worktreeId: 'w', title: 'zsh', terminalId: 't1');
    await tester.pumpWidget(
      MaterialApp(
        home: TerminalScreen(model: _Model(_RefusingRpc()), tab: tab),
      ),
    );
    // 错误在第一次订阅时就到；只等几毫秒，不走到尺寸稳定后的重订
    for (var i = 0; i < 5; i++) {
      await tester.pump(const Duration(milliseconds: 10));
    }
    expect(find.byType(NoticeBar), findsOneWidget);
    await tester.pumpWidget(const SizedBox());
  });
}
