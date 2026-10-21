import 'package:flterm/flterm.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

Future<FocusNode> _pumpTerminal(
  WidgetTester tester,
  VoidCallback? onTap,
) async {
  final controller = TerminalController();
  addTearDown(controller.dispose);
  final focus = FocusNode();
  addTearDown(focus.dispose);
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: TerminalView(
          controller: controller,
          focusNode: focus,
          onTap: onTap,
        ),
      ),
    ),
  );
  return focus;
}

void main() {
  testWidgets('没设 onTap 时轻点终端照常聚焦', (tester) async {
    final focus = await _pumpTerminal(tester, null);
    await tester.tap(find.byType(TerminalView));
    await tester.pump();
    expect(focus.hasFocus, isTrue);
  });

  testWidgets('设了 onTap 时轻点终端交给外层，不自己聚焦', (tester) async {
    var taps = 0;
    final focus = await _pumpTerminal(tester, () => taps++);
    await tester.tap(find.byType(TerminalView));
    await tester.pump();
    expect(taps, 1);
    expect(focus.hasFocus, isFalse);
  });
}
