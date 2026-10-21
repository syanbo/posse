import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';

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
    final model = HiveAppModel(
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
}
