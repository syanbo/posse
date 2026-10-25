import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/media_pager.dart';

void main() {
  Widget pager(ValueChanged<int> onChanged) => MaterialApp(
    home: Scaffold(
      body: MediaPager(
        count: 3,
        initialIndex: 0,
        onChanged: onChanged,
        builder: (_, i) => ZoomImage(
          child: SizedBox(
            width: 200,
            height: 200,
            child: ColoredBox(color: Colors.red, child: Text('p$i')),
          ),
        ),
      ),
    ),
  );

  testWidgets('swipes between pages, zoomed image locks paging', (
    tester,
  ) async {
    var page = 0;
    await tester.pumpWidget(pager((i) => page = i));
    expect(find.text('p0'), findsOneWidget);

    await tester.fling(find.text('p0'), const Offset(-400, 0), 1500);
    await tester.pumpAndSettle();
    expect(page, 1);
    expect(find.text('p1'), findsOneWidget);
    expect(find.text('p0'), findsNothing, reason: '翻走的页被释放');

    // 双指放大
    final center = tester.getCenter(find.text('p1'));
    final a = await tester.startGesture(center - const Offset(20, 0));
    final b = await tester.startGesture(center + const Offset(20, 0));
    // 第二根手指落下到动起来之间隔着一帧
    await tester.pump();
    for (var i = 0; i < 5; i++) {
      await a.moveBy(const Offset(-20, 0));
      await b.moveBy(const Offset(20, 0));
      await tester.pump();
    }
    await a.up();
    await b.up();
    await tester.pumpAndSettle();

    final box = find.descendant(
      of: find.byType(ZoomImage),
      matching: find.byType(Transform),
    );
    expect(
      tester.widget<Transform>(box).transform.getMaxScaleOnAxis(),
      greaterThan(2),
      reason: '双指放大了',
    );

    await tester.fling(find.text('p1'), const Offset(-400, 0), 1500);
    await tester.pumpAndSettle();
    expect(page, 1, reason: '放大后拖动只移动图片，不翻页');
  });
}
