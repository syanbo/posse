import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/l10n/l10n.dart';

void main() {
  final zh = lookupAppLocalizations(const Locale('zh'));
  final en = lookupAppLocalizations(const Locale('en'));
  final at = DateTime(2026, 10, 23, 18, 4, 5);

  test('clock follows the 12/24-hour setting in both languages', () {
    expect(clockTime(en, at, h24: true), '18:04');
    expect(clockTime(zh, at, h24: true), '18:04');
    expect(clockTime(en, at, h24: false), '6:04 PM');
    expect(clockTime(zh, at, h24: false), '下午6:04');
    expect(clockTime(zh, DateTime(2026, 1, 2, 8, 5), h24: true), '08:05');
    expect(clockTime(zh, DateTime(2026, 1, 2, 8, 5), h24: false), '上午8:05');
    expect(monthDayTime(en, at, h24: true), 'Oct 23 18:04');
    expect(monthDayTime(zh, at, h24: true), '10月23日 18:04');
    expect(monthDayTime(en, at, h24: false), 'Oct 23 6:04 PM');
    expect(monthDayTime(zh, at, h24: false), '10月23日 下午6:04');
  });

  testWidgets('use24Hour reads the system setting', (tester) async {
    late bool h24;
    Widget app() => MaterialApp(
      home: Builder(
        builder: (context) {
          h24 = use24Hour(context);
          return const SizedBox();
        },
      ),
    );
    await tester.pumpWidget(app());
    expect(h24, isTrue); // flutter_test_config 把测试固定在 24 小时制
    // 测试的设值不通知界面，照系统改了设置时那样让 MediaQuery 重读
    tester.platformDispatcher.alwaysUse24HourFormatTestValue = false;
    tester.binding.handleAccessibilityFeaturesChanged();
    await tester.pump();
    expect(h24, isFalse);
  });
}
