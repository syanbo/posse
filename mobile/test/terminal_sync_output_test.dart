import 'dart:async';
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

  // 清屏加清回滚后程序把整段内容从头重写，经网络分好几段到
  const clear = '\x1b[H\x1b[2J\x1b[3J';
  String lines(int n) => List.generate(n, (i) => 'line $i').join('\r\n');

  testWidgets('回滚被清掉后等输出停下再画', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, '${clear}one\r\ntwo');
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 40));
    expect(rows, isEmpty);

    // 后面一段接着到，要再等它停下
    _write(controller, '\r\nthree');
    await tester.pump(const Duration(milliseconds: 40));
    expect(rows, isEmpty);

    await tester.pump(const Duration(milliseconds: 150));
    await tester.pump();
    expect(rows, [3]);
  });

  testWidgets('回滚被清掉后输出一直不停，最多等一秒', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, '${clear}one');
    // 每 20ms 到一段，计时器到点和出帧之间也有输出进来；接在同一行，画出来是一行
    final feed = Timer.periodic(
      const Duration(milliseconds: 20),
      (_) => _write(controller, '.'),
    );
    addTearDown(feed.cancel);
    for (var i = 0; i < 9; i++) {
      await tester.pump(const Duration(milliseconds: 100));
    }
    expect(rows, isEmpty);

    await tester.pump(const Duration(milliseconds: 150));
    expect(rows, [1]);
    feed.cancel();
  });

  testWidgets('切在两段之间的清回滚也认得出', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, '\x1b[H\x1b[2J\x1b[3');
    _write(controller, 'Jone\r\ntwo');
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 30));
    expect(rows, isEmpty);

    await tester.pump(const Duration(milliseconds: 150));
    await tester.pump();
    expect(rows, [2]);
  });

  testWidgets('只清屏不清回滚时照常画', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, '\x1b[H\x1b[2Jone\r\ntwo');
    await tester.pump();
    await tester.pump();
    expect(rows, [2]);
  });

  testWidgets('清回滚后下一段切进备用屏幕时不再等', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, '${clear}main');
    await tester.pump(const Duration(milliseconds: 10));
    _write(controller, '\x1b[?1049hALT');
    await tester.pump();
    await tester.pump();
    expect(rows, [1]);
  });

  testWidgets('同步更新快到一秒时才清回滚，到点不算新输出', (tester) async {
    final (controller, rows) = await _pumpTerminal(tester);
    _write(controller, lines(300));
    await tester.pump();
    await tester.pump();
    rows.clear();

    _write(controller, _begin);
    await tester.pump(const Duration(milliseconds: 900));
    _write(controller, '${clear}one\r\ntwo');
    // 清回滚的安静检查在 960ms 看到这次写入、1020ms 放行；同步更新 1000ms 到点不该再推后
    await tester.pump(const Duration(milliseconds: 130));
    await tester.pump();
    expect(rows, [2]);
  });
}
