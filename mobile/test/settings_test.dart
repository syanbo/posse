import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';

void main() {
  test('settings default to dark and terminal, and ignore bad values', () {
    final d = AppSettings.decode(null);
    expect(d.theme, 'dark');
    expect(d.openMode, 'terminal');
    final bad = AppSettings.decode(
      jsonEncode({'theme': 'pink', 'openMode': 1, 'terminalFontSize': 99}),
    );
    expect(bad.theme, 'dark');
    expect(bad.openMode, 'terminal');
    expect(bad.terminalFontSize, 18);
    expect(AppSettings.decode('not json').theme, 'dark');
  });

  test('updating a setting saves it', () async {
    final store = MemorySettingsStore();
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: store,
    );
    model.updateSettings((s) {
      s.theme = 'light';
      s.openMode = 'chat';
      s.showReasoning = false;
    });
    await Future<void>.delayed(Duration.zero);
    final back = AppSettings.decode(store.data);
    expect(back.theme, 'light');
    expect(back.openMode, 'chat');
    expect(back.showReasoning, isFalse);
  });

  test(
    'desk watch and the trackpad are on by default, once for old settings',
    () async {
      expect(AppSettings().deskWatch, isTrue);
      expect(AppSettings().deskTrackpad, isTrue);
      expect(
        AppSettings.decode(
          jsonEncode({'deskVersion': deskVersion, 'deskWatch': 'yes'}),
        ).deskWatch,
        isTrue,
      );
      // 实验时存下的是旧默认值：换成新的，换了操作方式再提示一次手势
      final old = AppSettings.decode(
        jsonEncode({
          'deskWatch': false,
          'deskTrackpad': false,
          'deskGesturesSeen': true,
        }),
      );
      expect(old.deskWatch, isTrue);
      expect(old.deskTrackpad, isTrue);
      expect(old.deskGesturesSeen, isFalse);
      // 新版之后自己关掉的照旧
      final chosen = AppSettings.decode(
        jsonEncode({
          'deskVersion': deskVersion,
          'deskWatch': false,
          'deskTrackpad': false,
          'deskGesturesSeen': true,
        }),
      );
      expect(chosen.deskWatch, isFalse);
      expect(chosen.deskTrackpad, isFalse);
      expect(chosen.deskGesturesSeen, isTrue);

      final store = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      model.updateSettings((s) => s.deskWatch = false);
      await Future<void>.delayed(Duration.zero);
      expect(AppSettings.decode(store.data).deskWatch, isFalse);
    },
  );
}
