import 'dart:convert';

import 'package:flterm/flterm.dart' hide Key;
// ignore: implementation_imports
import 'package:flterm/src/rendering.dart' show measureCellMetrics;
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/terminal/selection_chrome.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  testWidgets('拖一端把手拉长选区，点空白取消', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller));
    await tester.pump();
    controller.write(utf8.encode('hello world\r\n'));
    await tester.pump();

    controller.selectRange(
      start: const Position(row: 0, col: 0),
      end: const Position(row: 0, col: 4),
      pointTag: .viewport,
    );
    await tester.pump();
    expect(controller.selectedText().trim(), 'hello');
    expect(find.text('复制'), findsOneWidget);
    expect(find.text('全选'), findsOneWidget);
    expect(find.byKey(const Key('selection-handle-start')), findsOneWidget);
    expect(find.byKey(const Key('selection-handle-end')), findsOneWidget);

    final before = controller.selectedText().trim();
    await tester.drag(
      find.byKey(const Key('selection-handle-end')),
      const Offset(90, 0),
    );
    await tester.pump();
    final after = controller.selectedText().trim();
    expect(after.startsWith(before), isTrue);
    expect(after.length, greaterThan(before.length));
    expect(controller.selectionEnds!.startCol, 0);

    await tester.tapAt(const Offset(30, 220));
    await tester.pump();
    expect(controller.hasSelection, isFalse);
    expect(find.text('复制'), findsNothing);
  });

  testWidgets('长按选出一个词，全选再扩大', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller));
    controller.write(utf8.encode('hello world\r\n'));
    await tester.pump();
    await tester.pump();

    final gesture = await tester.startGesture(const Offset(24, 8));
    await tester.pump(const Duration(milliseconds: 600));
    await gesture.up();
    await tester.pump();

    final word = controller.selectedText().trim();
    expect(word, isNotEmpty);
    expect(word.contains('\n'), isFalse);
    expect(find.text('复制'), findsOneWidget);

    await tester.tap(find.text('全选'));
    await tester.pump();
    expect(controller.selectedText().contains('hello world'), isTrue);
    expect(controller.selectedText().trim().length, greaterThan(word.length));
  });

  testWidgets('全选只扩到空行隔开的这一段', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller));
    await tester.pump();
    controller.write(utf8.encode('alpha beta\r\nsecond line\r\n\r\ngamma\r\n'));
    await tester.pump();

    controller.selectRange(
      start: const Position(row: 0, col: 0),
      end: const Position(row: 0, col: 4),
      pointTag: .viewport,
    );
    await tester.pump();
    await tester.tap(find.text('全选'));
    await tester.pump();

    final text = controller.selectedText();
    expect(text.contains('alpha beta'), isTrue);
    expect(text.contains('second line'), isTrue);
    expect(text.contains('gamma'), isFalse);
  });

  testWidgets('宽字符不会只选中半个', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller));
    await tester.pump();
    controller.write(utf8.encode('A你B\r\n'));
    await tester.pump();

    controller.selectRange(
      start: const Position(row: 0, col: 2),
      end: const Position(row: 0, col: 2),
      pointTag: .viewport,
    );
    expect(controller.selectionEnds!.startCol, 1);
    expect(controller.selectionEnds!.endCol, 2);
    expect(controller.selectedText().trim(), '你');

    controller.selectRange(
      start: const Position(row: 0, col: 0),
      end: const Position(row: 0, col: 1),
      pointTag: .viewport,
    );
    expect(controller.selectionEnds!.startCol, 0);
    expect(controller.selectionEnds!.endCol, 2);
    expect(controller.selectedText().trim(), 'A你');
  });

  testWidgets('回滚时钉住的那一端还在原来的字上', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller, height: 120));
    await tester.pump();
    final lines = List.generate(30, (i) => 'line $i');
    controller.write(utf8.encode('${lines.join('\r\n')}\r\n'));
    await tester.pump();

    controller.selectRange(
      start: const Position(row: 0, col: 0),
      end: const Position(row: 0, col: 3),
      pointTag: .viewport,
    );
    final pinned = controller.selectionEnds!.screenStart;
    final row = controller.selectionEnds!.startRow;
    controller.nudgeViewport(-1);
    await tester.pump();
    expect(controller.selectionEnds!.screenStart, pinned);
    expect(controller.selectionEnds!.startRow, isNot(row));
  });

  testWidgets('选区两端都滚出屏幕时复制按钮还在', (tester) async {
    final controller = TerminalController();
    addTearDown(controller.dispose);
    await tester.pumpWidget(_board(controller, height: 120));
    await tester.pump();
    final lines = List.generate(30, (i) => 'line $i');
    controller.write(utf8.encode('${lines.join('\r\n')}\r\n'));
    await tester.pump();

    controller.selectRange(
      start: const Position(row: 2, col: 0),
      end: const Position(row: 2, col: 3),
      pointTag: .viewport,
    );
    await tester.pump();
    await tester.tap(find.text('全选'));
    await tester.pump();
    expect(controller.selectedText().contains('line 0'), isTrue);
    expect(controller.selectionEnds, isNull);
    expect(find.text('复制'), findsOneWidget);
    expect(find.byKey(const Key('selection-handle-start')), findsNothing);

    controller.clearSelection();
    await tester.pump();
    expect(find.text('复制'), findsNothing);
  });
}

Widget _board(TerminalController controller, {double height = 280}) {
  return MaterialApp(
    home: Scaffold(
      body: Align(
        alignment: Alignment.topLeft,
        child: SizedBox(
          width: 420,
          height: height,
          child: LayoutBuilder(
            builder: (context, constraints) {
              const fontSize = 14.0;
              final theme = TerminalTheme.dark().copyWith(
                fontSize: fontSize,
                fontFamily: monoFamily,
                fontFamilyFallback: monoFallback,
              );
              final cell = measureCellMetrics(
                fontSize: fontSize,
                fontFamily: monoFamily,
                fontFamilyFallback: monoFallback,
                devicePixelRatio: MediaQuery.devicePixelRatioOf(context),
              );
              return Stack(
                children: [
                  TerminalView(
                    controller: controller,
                    theme: theme,
                    padding: EdgeInsets.zero,
                    showKeyboard: false,
                    autofocus: false,
                  ),
                  Positioned.fill(
                    child: SelectionChrome(
                      controller: controller,
                      width: constraints.maxWidth,
                      height: constraints.maxHeight,
                      covered: 0,
                      cellWidth: cell.cellWidth,
                      cellHeight: cell.cellHeight,
                      padLeft: 0,
                      padTop: 0,
                      lift: 0,
                      pinch: 1,
                      panX: 0,
                      panY: 0,
                      cropX: 0,
                    ),
                  ),
                ],
              );
            },
          ),
        ),
      ),
    ),
  );
}
