import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/workspace/new_session.dart';

// 新建会话面板只列电脑上能用的 agent（装了、又没在设置里关掉），加上普通终端；
// 列表变了，没点过的跟着选第一个，选中的被关掉了也换掉。
void main() {
  testWidgets('new session lists the computer agents', (tester) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.agents = const ['codex', 'cursor'];
    final ctx = GlobalKey();
    await tester.pumpWidget(
      MaterialApp(
        locale: const Locale('en'),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        home: Scaffold(key: ctx),
      ),
    );
    showNewSession(ctx.currentContext!, model).ignore();
    await tester.pumpAndSettle();

    expect(find.text('Codex'), findsOneWidget);
    expect(find.text('Cursor'), findsOneWidget);
    expect(find.text('Terminal'), findsOneWidget);
    expect(find.text('Claude'), findsNothing);
    expect(find.text('Grok'), findsNothing);
    expect(find.text('Start Chat with Codex'), findsOneWidget);

    // 电脑上关掉了 Codex：默认换成剩下的第一个
    model.agents = const ['cursor'];
    // ignore: invalid_use_of_protected_member, invalid_use_of_visible_for_testing_member
    model.notifyListeners();
    await tester.pumpAndSettle();
    expect(find.text('Codex'), findsNothing);
    expect(find.text('Start Chat with Cursor'), findsOneWidget);

    // 用户选了终端，列表再变也不换
    await tester.tap(find.text('Terminal'));
    await tester.pumpAndSettle();
    model.agents = const ['claude', 'cursor'];
    // ignore: invalid_use_of_protected_member, invalid_use_of_visible_for_testing_member
    model.notifyListeners();
    await tester.pumpAndSettle();
    expect(find.text('Open Terminal'), findsOneWidget);
    expect(find.text('Claude'), findsOneWidget);
  });
}
