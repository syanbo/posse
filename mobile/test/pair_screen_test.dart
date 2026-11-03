import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/workspace.dart';

void main() {
  testWidgets(
    'adding a computer keeps its failure after the model clears error',
    (tester) async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          locale: const Locale('en'),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          home: PairScreen(model: model, adding: true, manual: true),
        ),
      );
      final l = AppLocalizations.of(tester.element(find.byType(PairScreen)));
      await tester.enterText(find.byType(TextField), 'not a pairing code');
      await tester.scrollUntilVisible(
        find.text(l.pairConnect),
        200,
        scrollable: find.byType(Scrollable).first,
      );
      await tester.tap(find.text(l.pairConnect));
      await tester.pumpAndSettle();
      expect(find.text(l.errorBadPairingCode), findsOneWidget);

      // 已连着的那台电脑定时刷新成功，清掉共用的 error
      model.error = null;
      // ignore: invalid_use_of_protected_member
      model.notifyListeners();
      await tester.pumpAndSettle();
      expect(find.text(l.errorBadPairingCode), findsOneWidget);
    },
  );
}
