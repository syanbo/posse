import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/image_pick.dart';
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

    testWidgets('confirm opens as bottom sheet with warning icon and tones', (
      tester,
    ) async {
      await tester.pumpWidget(confirmHost((_) {}, tone: ConfirmTone.warning));
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      // 确认框统一从底部作为 BottomSheet 弹出，不再是居中 Dialog。
      expect(find.byType(BottomSheet), findsOneWidget);
      expect(find.byType(Dialog), findsNothing);
      expect(find.byIcon(Icons.warning_amber_rounded), findsOneWidget);
      expect(find.text('切换分支？'), findsOneWidget);
      expect(find.text('仍然切换'), findsOneWidget);
      final sheetRect = tester.getRect(find.byType(BottomSheet));
      final screenSize = tester.view.physicalSize / tester.view.devicePixelRatio;
      expect(sheetRect.bottom, closeTo(screenSize.height, 1.0));
    });

    testWidgets('reduced motion shows bottom sheet immediately without slide', (
      tester,
    ) async {
      await tester.pumpWidget(
        confirmHost((_) {}, tone: ConfirmTone.warning, reduceMotion: true),
      );
      await tester.tap(find.text('open'));
      await tester.pump();
      expect(find.byType(BottomSheet), findsOneWidget);
      expect(find.text('切换分支？'), findsOneWidget);
      expect(find.text('仍然切换'), findsOneWidget);
    });

    testWidgets('confirm buttons stay above soft keyboard viewInsets', (
      tester,
    ) async {
      tester.view.physicalSize = const Size(740, 360);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);

      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          builder: (context, child) => MediaQuery(
            data: MediaQuery.of(context).copyWith(
              viewInsets: const EdgeInsets.only(bottom: 160),
              padding: const EdgeInsets.only(bottom: 20),
            ),
            child: child!,
          ),
          home: Builder(
            builder: (context) => TextButton(
              onPressed: () => showPosseConfirm(
                context,
                title: '关闭这个会话？',
                body: '终端进程会被结束。',
                confirm: '关闭',
                tone: ConfirmTone.danger,
              ),
              child: const Text('open'),
            ),
          ),
        ),
      );
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      final btnRect = tester.getRect(find.text('关闭'));
      // 360 高的窗口被 160 高软键盘挡住底部，确认按钮必须完全位于 y <= 200 的可见区上方。
      expect(btnRect.bottom, lessThanOrEqualTo(200.0));
    });

    testWidgets(
      'confirm sheet avoids overflow when keyboard rises dynamically or text is large',
      (tester) async {
        tester.view.physicalSize = const Size(740, 360);
        tester.view.devicePixelRatio = 1.0;
        addTearDown(tester.view.reset);

        final inset = ValueNotifier<double>(0);
        addTearDown(inset.dispose);

        await tester.pumpWidget(
          ValueListenableBuilder<double>(
            valueListenable: inset,
            builder: (_, kb, _) => MaterialApp(
              locale: const Locale('zh'),
              localizationsDelegates: AppLocalizations.localizationsDelegates,
              supportedLocales: AppLocalizations.supportedLocales,
              builder: (context, child) => MediaQuery(
                data: MediaQuery.of(context).copyWith(
                  viewInsets: EdgeInsets.only(bottom: kb),
                  padding: const EdgeInsets.only(bottom: 20),
                  textScaler: const TextScaler.linear(2.0),
                ),
                child: child!,
              ),
              home: Builder(
                builder: (context) => TextButton(
                  onPressed: () => showPosseConfirm(
                    context,
                    title: '关闭这个会话？',
                    body: '终端进程会被结束。',
                    confirm: '关闭',
                    tone: ConfirmTone.danger,
                  ),
                  child: const Text('open'),
                ),
              ),
            ),
          ),
        );

        await tester.tap(find.text('open'));
        await tester.pumpAndSettle();
        expect(tester.takeException(), isNull);

        // 打开后键盘再顶起至 240（仅剩 120 可用高度）+ 2.0 大字：不得发生 RenderFlex 溢出。
        inset.value = 240;
        await tester.pumpAndSettle();
        expect(tester.takeException(), isNull);
        expect(find.byType(BottomSheet), findsOneWidget);
      },
    );

    testWidgets('file attachment sheet lifts above persistent soft keyboard', (
      tester,
    ) async {
      tester.view.physicalSize = const Size(740, 360);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);

      FileAsk? picked;
      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          builder: (context, child) => MediaQuery(
            data: MediaQuery.of(context).copyWith(
              viewInsets: const EdgeInsets.only(bottom: 160),
              padding: const EdgeInsets.only(bottom: 20),
            ),
            child: child!,
          ),
          home: Builder(
            builder: (context) => TextButton(
              onPressed: () async {
                picked = await askFileSource(context);
              },
              child: const Text('open'),
            ),
          ),
        ),
      );
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      // 整个滚动视口必须完全位于键盘顶边（y <= 200）之上，滚动后末项完全可见且可点中。
      final scrollRect = tester.getRect(find.byType(SingleChildScrollView));
      expect(scrollRect.bottom, lessThanOrEqualTo(200.0));
      await tester.ensureVisible(find.byKey(const Key('file-ask-files')));
      await tester.pumpAndSettle();
      final lastRow = tester.getRect(find.byKey(const Key('file-ask-files')));
      expect(lastRow.bottom, lessThanOrEqualTo(200.0));
      await tester.tap(find.byKey(const Key('file-ask-files')));
      await tester.pumpAndSettle();
      expect(picked, FileAsk.files);
    });
  });
}
