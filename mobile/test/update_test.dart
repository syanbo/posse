import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/update/update.dart';

Map<String, Object?> _latest(int build) => {
  'version': '0.1.1',
  'build': build,
  'size': '14.6 MB',
  'notes': ['历史页分组可以展开收起', '首页提示新版本'],
  'install': 'itms-services://?action=download-manifest&url=x',
};

void main() {
  test('parses latest.json and skips broken ones', () {
    final r = AppRelease.parse(_latest(460))!;
    expect(r.version, '0.1.1');
    expect(r.build, 460);
    expect(r.notes, hasLength(2));
    expect(AppRelease.parse({'version': '0.1.1'}), isNull);
    expect(AppRelease.parse('x'), isNull);
  });

  test('only a newer build counts, and not-ad-hoc builds never ask', () async {
    var asked = 0;
    final newer = UpdateChecker(
      build: 453,
      fetch: () async {
        asked++;
        return _latest(460);
      },
    );
    await newer.check();
    expect(newer.latest?.build, 460);
    // 6 小时内回到前台不再查
    await newer.check();
    expect(asked, 1);

    final same = UpdateChecker(build: 460, fetch: () async => _latest(460));
    await same.check();
    expect(same.latest, isNull);

    final debug = UpdateChecker(
      build: 0,
      fetch: () async {
        asked++;
        return _latest(460);
      },
    );
    await debug.check();
    expect(debug.latest, isNull);
    expect(asked, 1);

    // 网络不通不报错，下次接着查
    final offline = UpdateChecker(
      build: 453,
      fetch: () async => throw Exception('offline'),
    );
    await offline.check();
    expect(offline.latest, isNull);
  });

  testWidgets('the home banner opens the notes and × hides this build', (
    tester,
  ) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    final checker = UpdateChecker(build: 453, fetch: () async => _latest(460));
    await checker.check();
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          body: ListenableBuilder(
            listenable: model,
            builder: (_, _) => UpdateBanner(model: model, checker: checker),
          ),
        ),
      ),
    );
    expect(
      find.text('新版本 0.1.1（460） · 14.6 MB', findRichText: true),
      findsOneWidget,
    );

    await tester.tap(find.text('新版本 0.1.1（460） · 14.6 MB', findRichText: true));
    await tester.pumpAndSettle();
    expect(find.text('历史页分组可以展开收起'), findsOneWidget);
    expect(find.text('更新'), findsOneWidget);
    await tester.tapAt(const Offset(200, 20));
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('不再提示这个版本'));
    await tester.pump();
    expect(model.settings.skippedUpdateBuild, 460);
    expect(find.textContaining('新版本'), findsNothing);
  });
}
