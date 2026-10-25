import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/key_bar.dart';
import 'package:posse_mobile/desk/mods.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  testWidgets('a narrow key bar fades the cut-off end until it is scrolled', (
    tester,
  ) async {
    await _pump(tester, 320);
    await tester.pump();
    expect(find.byKey(const Key('desk-key-fade')), findsOneWidget);

    await tester.drag(find.byType(ListView), const Offset(-800, 0));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-key-fade')), findsNothing);
  });

  testWidgets('a wide key bar shows every key without a fade', (tester) async {
    await _pump(tester, 900);
    await tester.pump();
    expect(find.byKey(const Key('desk-key-fade')), findsNothing);
    expect(find.text('⌃'), findsOneWidget);
  });
}

Future<void> _pump(WidgetTester tester, double width) {
  return tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(Brightness.dark),
      home: Scaffold(
        body: Align(
          alignment: Alignment.bottomCenter,
          child: SizedBox(
            width: width,
            child: DeskKeyBar(
              mods: DeskMods(),
              keyboardUp: false,
              onKeyboard: () {},
              onKey: (_) {},
              onModTap: (_) {},
              onModLock: (_) {},
            ),
          ),
        ),
      ),
    ),
  );
}
