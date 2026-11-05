import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/ui/widgets.dart';

import 'shot_fonts.dart';

// 三种语气的确认框的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/dialog_shots_test.dart

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('confirm dialog screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    for (final dark in [false, true]) {
      for (final tone in ConfirmTone.values) {
        final key = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: MaterialApp(
              debugShowCheckedModeBanner: false,
              theme: shotTheme(
                posseTheme(dark ? Brightness.dark : Brightness.light),
              ),
              locale: const Locale('zh'),
              localizationsDelegates: AppLocalizations.localizationsDelegates,
              supportedLocales: AppLocalizations.supportedLocales,
              home: Builder(
                builder: (context) {
                  final l = context.l10n;
                  return Scaffold(
                    body: Center(
                      child: TextButton(
                        onPressed: () => switch (tone) {
                          ConfirmTone.warning => showPosseConfirm(
                            context,
                            title: l.gitCheckoutAgentTitle('Claude'),
                            body: l.gitCheckoutAgentBody,
                            confirm: l.gitCheckoutAnyway,
                            tone: tone,
                          ),
                          ConfirmTone.danger => showPosseConfirm(
                            context,
                            title: l.sessionCloseSessionConfirm,
                            body: l.sessionCloseSessionBody('Claude'),
                            confirm: l.commonClose,
                            tone: tone,
                          ),
                          ConfirmTone.normal => showPosseConfirm(
                            context,
                            title: l.chatCompactConfirm,
                            body: l.chatCompactBody(82, 'Claude'),
                            confirm: l.chatCompactButton,
                          ),
                        },
                        child: const Text('open'),
                      ),
                    ),
                  );
                },
              ),
            ),
          ),
        );
        await tester.tap(find.text('open'));
        await tester.pumpAndSettle();
        if (dir.isEmpty) continue;
        await tester.runAsync(() async {
          final boundary =
              key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
          final image = await boundary.toImage(pixelRatio: 3);
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          File('$dir/confirm-${tone.name}-${dark ? 'dark' : 'light'}.png')
              .writeAsBytesSync(bytes!.buffer.asUint8List());
        });
      }
    }
  });
}
