import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
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
    expect(r.notesEn, isEmpty);
    expect(AppRelease.parse({'version': '0.1.1'}), isNull);
    expect(AppRelease.parse('x'), isNull);
  });

  test('picks English notes for an English interface when there are any', () {
    final zh = lookupAppLocalizations(const Locale('zh'));
    final en = lookupAppLocalizations(const Locale('en'));
    final both = AppRelease.parse({
      ..._latest(460),
      'notes_en': ['History groups collapse', '', 3, 'Home shows new versions'],
    })!;
    expect(both.notesEn, [
      'History groups collapse',
      'Home shows new versions',
    ]);
    expect(both.notesFor(en), both.notesEn);
    expect(both.notesFor(zh), ['历史页分组可以展开收起', '首页提示新版本']);
    // 老的 latest.json 没有 notes_en，或是写坏了：英文界面也显示中文
    final old = AppRelease.parse(_latest(460))!;
    expect(old.notesFor(en), old.notes);
    final broken = AppRelease.parse({..._latest(460), 'notes_en': 'x'})!;
    expect(broken.notesFor(en), broken.notes);
    final empty = AppRelease.parse({..._latest(460), 'notes_en': <String>[]})!;
    expect(empty.notesFor(en), empty.notes);
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

  test(
    'a forced check skips the 6-hour wait and joins a running one',
    () async {
      var asked = 0;
      final checker = UpdateChecker(
        build: 460,
        fetch: () async {
          asked++;
          return _latest(asked == 1 ? 460 : 470);
        },
      );
      expect(await checker.check(), isTrue);
      expect(checker.latest, isNull);
      expect(await checker.check(), isTrue);
      expect(asked, 1);

      final a = checker.check(force: true);
      expect(checker.checking, isTrue);
      final b = checker.check(force: true);
      expect(await a, isTrue);
      expect(await b, isTrue);
      expect(asked, 2);
      expect(checker.checking, isFalse);
      expect(checker.latest?.build, 470);

      final offline = UpdateChecker(
        build: 453,
        fetch: () async => throw Exception('offline'),
      );
      expect(await offline.check(force: true), isFalse);
      expect(offline.checking, isFalse);
    },
  );

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
