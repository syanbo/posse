import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/settings/custom_keys.dart';

CustomKey _key(
  String kind,
  String value, {
  String label = '',
  bool enter = false,
}) => CustomKey(kind: kind, label: label, value: value, enter: enter);

void main() {
  test('custom keys turn into terminal bytes', () {
    expect(_key('text', '/compact').bytes, utf8.encode('/compact'));
    expect(_key('ctrl', 'r').bytes, [0x12]);
    expect(_key('ctrl', 'D').bytes, [0x04]);
    expect(_key('ctrl', '[').bytes, [0x1b]);
    expect(_key('ctrl', '?').bytes, [0x7f]);
    expect(_key('ctrl', '1').bytes, isEmpty);
    expect(_key('ctrl', '').bytes, isEmpty);
    expect(_key('alt', 'b').bytes, [0x1b, 0x62]);
    expect(_key('special', 'PgUp').bytes, [0x1b, 0x5b, 0x35, 0x7e]);
    expect(_key('special', 'nope').bytes, isEmpty);
    expect(_key('text', '').valid, isFalse);
  });

  test('a key without a label shows what it sends', () {
    expect(_key('ctrl', 'r').shown, '^R');
    expect(_key('alt', 'f').shown, '⌥F');
    expect(_key('special', 'Home').shown, 'Home');
    expect(_key('text', '/compact').shown, '/compact');
    expect(_key('text', 'a very long command line').shown, 'a very long …');
    expect(_key('text', 'git status', label: ' gs ').shown, 'gs');
  });

  test(
    'custom keys survive a settings round trip and bad entries are dropped',
    () {
      final s = AppSettings(
        customKeys: [
          _key('text', '/clear', enter: true),
          _key('ctrl', 'r', label: '搜索'),
        ],
      );
      final back = AppSettings.decode(
        jsonEncode({
          ...s.toJson(),
          'customKeys': [
            ...(s.toJson()['customKeys'] as List),
            {'kind': 'ctrl', 'value': '1'},
            {'kind': 'weird', 'value': 'x'},
            'junk',
          ],
        }),
      );
      expect(back.customKeys.length, 2);
      expect(back.customKeys[0].value, '/clear');
      expect(back.customKeys[0].enter, isTrue);
      expect(back.customKeys[1].label, '搜索');
      expect(back.customKeys[1].bytes, [0x12]);
      expect(AppSettings.decode(null).customKeys, isEmpty);
    },
  );
}
