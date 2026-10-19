import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/settings/custom_keys.dart';
import 'package:hive_mobile/settings/custom_keys_screen.dart';
import 'package:hive_mobile/ui/theme.dart';

void main() {
  testWidgets('built-in keys are listed for sorting and the order resets', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1170, 4000);
    addTearDown(tester.view.reset);
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.dark),
        home: CustomKeysScreen(model: model),
      ),
    );

    // 内置键都在表里，默认顺序时没有"恢复默认顺序"
    expect(find.text('Esc：中断、退出'), findsOneWidget);
    expect(find.text('发送图片'), findsOneWidget);
    expect(find.text('恢复默认顺序'), findsNothing);
    // 内置键点了不打开编辑面板
    await tester.tap(find.text('Esc：中断、退出'));
    await tester.pumpAndSettle();
    expect(find.text('编辑快捷键'), findsNothing);

    // 把 Esc 拖到最上面
    final handles = find.byIcon(Icons.drag_handle_rounded);
    final esc = tester.getCenter(handles.at(2));
    final top = tester.getCenter(handles.at(0));
    final drag = await tester.startGesture(esc);
    await tester.pump(const Duration(milliseconds: 100));
    // 一步拖太远认不出拖动，分几步挪上去
    for (var i = 1; i <= 10; i++) {
      await drag.moveTo(Offset.lerp(esc, top - const Offset(0, 30), i / 10)!);
      await tester.pump(const Duration(milliseconds: 16));
    }
    await tester.pump(const Duration(milliseconds: 300));
    await drag.up();
    await tester.pumpAndSettle();
    expect(model.settings.barKeys.first.builtin, 'Esc');

    await tester.tap(find.text('恢复默认顺序'));
    await tester.pumpAndSettle();
    expect([for (final k in model.settings.barKeys) k.builtin], builtinBarKeys);
    expect(find.text('恢复默认顺序'), findsNothing);
  });
}
