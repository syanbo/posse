import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/l10n/l10n.dart';
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
    expect(scaleOf(tester), inExclusiveRange(0.94, 1.0));
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

  group('confirm', () {
    Widget confirmHost(
      ValueChanged<bool> onResult, {
      ConfirmTone tone = ConfirmTone.normal,
      bool reduceMotion = false,
    }) {
      return MaterialApp(
        locale: const Locale('zh'),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        builder: (context, child) => MediaQuery(
          data: MediaQuery.of(context)
              .copyWith(disableAnimations: reduceMotion),
          child: child!,
        ),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async => onResult(
              await showPosseConfirm(
                context,
                title: '切换分支？',
                body: '说明',
                confirm: '仍然切换',
                tone: tone,
              ),
            ),
            child: const Text('open'),
          ),
        ),
      );
    }

    testWidgets('confirm returns true, cancel and barrier false', (
      tester,
    ) async {
      final results = <bool>[];
      await tester.pumpWidget(confirmHost(results.add));
      for (final target in ['仍然切换', '取消']) {
        await tester.tap(find.text('open'));
        await tester.pumpAndSettle();
        await tester.tap(find.text(target));
        await tester.pumpAndSettle();
      }
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      await tester.tapAt(const Offset(4, 4));
      await tester.pumpAndSettle();
      expect(results, [true, false, false]);
    });

    testWidgets('content appears in layers, buttons last', (tester) async {
      await tester.pumpWidget(confirmHost((_) {}, tone: ConfirmTone.warning));
      await tester.tap(find.text('open'));
      await tester.pump();
      // 160ms：文字刚开始出现，按钮还没出来
      await tester.pump(const Duration(milliseconds: 170));
      double opacityAbove(String text) => tester
          .widget<Opacity>(
            find
                .ancestor(of: find.text(text), matching: find.byType(Opacity))
                .first,
          )
          .opacity;
      expect(opacityAbove('切换分支？'), greaterThan(0));
      expect(opacityAbove('仍然切换'), 0);
      expect(find.byIcon(Icons.warning_amber_rounded), findsOneWidget);
      // 落定后各层都是原样，警示图标的子树没有重建过
      final icon = tester.element(find.byIcon(Icons.warning_amber_rounded));
      await tester.pumpAndSettle();
      expect(opacityAbove('切换分支？'), 1);
      expect(opacityAbove('仍然切换'), 1);
      expect(
        tester.element(find.byIcon(Icons.warning_amber_rounded)),
        same(icon),
      );
    });

    testWidgets('reduced motion shows everything at once', (tester) async {
      await tester.pumpWidget(
        confirmHost((_) {}, tone: ConfirmTone.warning, reduceMotion: true),
      );
      await tester.tap(find.text('open'));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 20));
      expect(
        find.ancestor(of: find.text('仍然切换'), matching: find.byType(Opacity)),
        findsNothing,
      );
      await tester.pumpAndSettle();
    });
  });
}
