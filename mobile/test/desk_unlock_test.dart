import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/unlock.dart';
import 'package:posse_mobile/ui/theme.dart';

Future<Future<MacPasswordAnswer?> Function()> _open(
  WidgetTester tester, {
  bool canRemember = true,
  bool saved = false,
}) async {
  MacPasswordAnswer? answer;
  var done = false;
  await tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(Brightness.dark),
      home: Builder(
        builder: (context) => TextButton(
          onPressed: () async {
            answer = await askMacPassword(
              context,
              canRemember: canRemember,
              saved: saved,
            );
            done = true;
          },
          child: const Text('open'),
        ),
      ),
    ),
  );
  return () async {
    expect(done, isTrue);
    return answer;
  };
}

void main() {
  test('one unlock at a time: a second tap within the wait is ignored', () {
    final gate = DeskUnlockGate();
    final at = DateTime(2026, 11, 4, 9);
    expect(gate.busy(at), isFalse);
    gate.sent(at);
    expect(gate.busy(at.add(const Duration(milliseconds: 200))), isTrue);
    expect(gate.busy(at.add(DeskUnlockGate.wait)), isFalse);
    // Mac 报了解锁就提前结束
    gate.sent(at);
    gate.clear();
    expect(gate.busy(at), isFalse);
  });

  test(
    'a password is saved only when it was typed and the Mac unlocked soon',
    () {
      final at = DateTime(2026, 11, 4, 9);
      final soon = at.add(const Duration(seconds: 3));
      expect(deskSaveOnUnlock(sentAt: at, now: soon, waitTyped: false), isTrue);
      // Mac 没报打完就解锁了：密码被丢掉，解锁的是别的途径
      expect(deskSaveOnUnlock(sentAt: at, now: soon, waitTyped: true), isFalse);
      expect(
        deskSaveOnUnlock(
          sentAt: at,
          now: at.add(const Duration(seconds: 16)),
          waitTyped: false,
        ),
        isFalse,
      );
    },
  );

  testWidgets('typed password comes back with the remember switch', (
    tester,
  ) async {
    final result = await _open(tester);
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-password-forget')), findsNothing);
    await tester.enterText(find.byKey(const Key('desk-password')), 'Pw!9');
    await tester.tap(find.byKey(const Key('desk-password-remember')));
    await tester.pump();
    await tester.tap(find.byKey(const Key('desk-unlock-send')));
    await tester.pumpAndSettle();
    final answer = await result();
    expect(answer?.password, 'Pw!9');
    expect(answer?.remember, isTrue);
  });

  testWidgets('a saved password starts remembered and can be forgotten', (
    tester,
  ) async {
    final result = await _open(tester, saved: true);
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    final remember = tester.widget<FilterChip>(
      find.byKey(const Key('desk-password-remember')),
    );
    expect(remember.selected, isTrue);
    await tester.tap(find.byKey(const Key('desk-password-forget')));
    await tester.pumpAndSettle();
    final answer = await result();
    expect(answer?.password, isNull);
    expect(answer?.remember, isFalse);
  });

  testWidgets('without a computer key there is nothing to remember', (
    tester,
  ) async {
    final result = await _open(tester, canRemember: false);
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-password-remember')), findsNothing);
    await tester.enterText(find.byKey(const Key('desk-password')), 'secret');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pumpAndSettle();
    final answer = await result();
    expect(answer?.password, 'secret');
    expect(answer?.remember, isFalse);
  });

  // 横屏 iPhone，键盘盖住下面 200 点：「记住」仍在键盘上方、点得到
  testWidgets('remember stays reachable above the keyboard in landscape', (
    tester,
  ) async {
    tester.view.devicePixelRatio = 3;
    tester.view.physicalSize = const Size(844 * 3, 390 * 3);
    tester.view.viewInsets = const FakeViewPadding(bottom: 200 * 3);
    addTearDown(tester.view.reset);
    final result = await _open(tester);
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    final chip = find.byKey(const Key('desk-password-remember'));
    expect(tester.getRect(chip).bottom, lessThanOrEqualTo(390 - 200));
    await tester.enterText(find.byKey(const Key('desk-password')), 'pw');
    await tester.tap(chip);
    await tester.pump();
    await tester.tap(find.byKey(const Key('desk-unlock-send')));
    await tester.pumpAndSettle();
    expect((await result())?.remember, isTrue);
  });
}
