import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/widgets.dart';

Widget host(ValueChanged<bool?> onResult, {bool reduceMotion = false}) {
  return MaterialApp(
    // 弹框推在根导航器上，减少动态效果要设在导航器外面
    builder: (context, child) => MediaQuery(
      data: MediaQuery.of(context).copyWith(disableAnimations: reduceMotion),
      child: child!,
    ),
    home: Builder(
      builder: (context) => TextButton(
        onPressed: () async {
          final ok = await showPosseDialog<bool>(
            context: context,
            builder: (dialog) => AlertDialog(
              title: const Text('关闭这个会话？'),
              actions: [
                TextButton(
                  onPressed: () => Navigator.pop(dialog, true),
                  child: const Text('关闭'),
                ),
              ],
            ),
          );
          onResult(ok);
        },
        child: const Text('open'),
      ),
    ),
  );
}

double scaleOf(WidgetTester tester) {
  final scale = tester.widget<ScaleTransition>(
    find.ancestor(
      of: find.byType(AlertDialog),
      matching: find.byType(ScaleTransition),
    ),
  );
  return scale.scale.value;
}

void main() {
  testWidgets('dialog grows in, then returns the chosen value', (tester) async {
    bool? result;
    await tester.pumpWidget(host((ok) => result = ok));
    await tester.tap(find.text('open'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 60));
    expect(scaleOf(tester), inExclusiveRange(0.9, 1.0));
    await tester.pumpAndSettle();
    expect(scaleOf(tester), 1.0);

    await tester.tap(find.text('关闭'));
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(result, isTrue);
  });

  testWidgets('reduced motion only fades', (tester) async {
    await tester.pumpWidget(host((_) {}, reduceMotion: true));
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(
      find.ancestor(
        of: find.byType(AlertDialog),
        matching: find.byType(ScaleTransition),
      ),
      findsNothing,
    );
  });
}
