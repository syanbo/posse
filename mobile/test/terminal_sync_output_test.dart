import 'dart:convert';
import 'dart:typed_data';

import 'package:flterm/flterm.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// 画到第几行为止有内容，每出一帧变了就报一次。
Future<(TerminalController, List<int>)> _pumpTerminal(
  WidgetTester tester,
) async {
  final controller = TerminalController();
  addTearDown(controller.dispose);
  final rows = <int>[];
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: TerminalView(controller: controller, onContentRows: rows.add),
      ),
    ),
  );
  await tester.pump();
  rows.clear();
  return (controller, rows);
}

void _write(TerminalController controller, String text) =>
    controller.write(Uint8List.fromList(utf8.encode(text)));

const _begin = '\x1b[?2026h';
const _end = '\x1b[?2026l';

void main() {
  testWidgets('同步更新没结束时不画新内容，结束后一起画出来', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, '$_begin\r\n\r\n\r\n\r\nfive');
    await tester.pump();
    expect(rows, isEmpty);

    _write(controller, _end);
    await tester.pump();
    await tester.pump();
    expect(rows, [5]);
  });

  testWidgets('同步更新超过一秒没结束就照常画', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, '$_begin\r\n\r\nthree');
    await tester.pump();
    expect(rows, isEmpty);

    await tester.pump(const Duration(seconds: 1));
    await tester.pump();
    expect(rows, [3]);
  });

  testWidgets('不在同步更新里时照常画', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, '\r\ntwo');
    await tester.pump();
    await tester.pump();
    expect(rows, [2]);
  });
}
