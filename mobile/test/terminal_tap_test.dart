import 'dart:convert';

import 'package:flterm/flterm.dart';
// ignore: implementation_imports
import 'package:flterm/src/rendering.dart' show measureCellMetrics;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
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

  testWidgets('键盘复制把终端选区交给外层', (tester) async {
    final controller = TerminalController();
    final focus = FocusNode();
    addTearDown(controller.dispose);
    addTearDown(focus.dispose);
    var copies = 0;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: TerminalView(
            controller: controller,
            focusNode: focus,
            onCopy: () => copies++,
          ),
        ),
      ),
    );
    focus.requestFocus();
    await tester.pump();
    controller.write(utf8.encode('first\r\nsecond'));
    controller.selectRange(
      start: const Position(row: 0, col: 0),
      end: const Position(row: 1, col: 5),
    );
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    expect(copies, 1);
  }, variant: const TargetPlatformVariant({TargetPlatform.iOS}));

  testWidgets('长按 OSC 8 续行取完整地址，纯文本仍可选中', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    var opened = 0;
    final addresses = <String>[];
    final theme = TerminalTheme.dark();
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: TerminalView(
            padding: EdgeInsets.zero,
            controller: controller,
            theme: theme,
            linkSettings: LinkSettings(onActivate: (_) => opened++),
            onHyperlinkLongPress: addresses.add,
          ),
        ),
      ),
    );
    const address = 'https://example.com/%2f?x=1&y=2';
    controller.write(
      utf8.encode(
        '\x1b]8;;$address\x1b\\visible\r\nlabel\x1b]8;;\x1b\\\r\n'
        'https://example.com/partial',
      ),
    );
    await tester.pump();
    final metrics = measureCellMetrics(
      fontSize: theme.fontSize,
      fontFamily: theme.fontFamily,
      fontFamilyFallback: theme.fontFamilyFallback,
      devicePixelRatio: tester.view.devicePixelRatio,
    );
    Offset centerOfRow(int row) =>
        Offset(metrics.cellWidth / 2, metrics.cellHeight * (row + 0.5));
    await tester.longPressAt(centerOfRow(1));
    await tester.pump();
    expect(addresses, [address]);
    expect(opened, 0);
    expect(controller.hasSelection, isFalse);
    await tester.longPressAt(centerOfRow(2));
    await tester.pump();
    expect(addresses, [address]);
    expect(controller.hasSelection, isTrue);
  });
}
