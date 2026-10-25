import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/widgets.dart';

void main() {
  testWidgets('推入动画结束才完成，不在动画里马上完成', (tester) async {
    final nav = GlobalKey<NavigatorState>();
    await tester.pumpWidget(
      MaterialApp(navigatorKey: nav, home: const Text('首页')),
    );
    late BuildContext page;
    nav.currentState!.push(
      MaterialPageRoute<void>(
        builder: (context) {
          page = context;
          return const Text('详情');
        },
      ),
    );
    await tester.pump();
    var done = false;
    routeSettled(page).then((_) => done = true);
    await tester.pump(const Duration(milliseconds: 100));
    expect(done, isFalse);
    await tester.pumpAndSettle();
    expect(done, isTrue);

    var again = false;
    routeSettled(page).then((_) => again = true);
    await tester.pump();
    expect(again, isTrue);
  });
}
