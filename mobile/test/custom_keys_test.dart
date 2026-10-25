import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/settings/custom_keys.dart';
import 'package:hive_mobile/terminal/frames.dart';

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

  test('the bar order survives a settings round trip, bad entries dropped', () {
    final s = AppSettings(
      barKeys: [
        const BarKey.builtin('Esc'),
        BarKey.custom(_key('text', '/clear', enter: true)),
        const BarKey.builtin('image'),
        BarKey.custom(_key('ctrl', 'r', label: '搜索')),
      ],
    );
    final back = AppSettings.decode(
      jsonEncode({
        ...s.toJson(),
        'barKeys': [
          ...(s.toJson()['barKeys'] as List),
          {'kind': 'ctrl', 'value': '1'},
          {'kind': 'weird', 'value': 'x'},
          {'builtin': 'nope'},
          {'builtin': 'Esc'},
          'junk',
        ],
      }),
    );
    final names = [for (final k in back.barKeys) k.builtin ?? k.custom!.shown];
    // 存下的顺序不变，重复的 Esc 去掉；没存的内置键插在默认顺序里它前面那个键后面
    expect(names, [
      'Esc',
      'Tab',
      'Enter',
      sendNowLabel,
      '⇧Tab',
      '^C',
      '清空',
      '退出',
      '↑',
      '↓',
      '←',
      '→',
      '/clear',
      'image',
      'paste',
      '搜索',
    ]);
    expect(back.customKeys.map((k) => k.bytes), [
      utf8.encode('/clear'),
      [0x12],
    ]);
    expect(back.customKeys.first.enter, isTrue);
  });

  test('old settings with only custom keys keep the old bar order', () {
    final back = AppSettings.decode(
      jsonEncode({
        'customKeys': [
          {'kind': 'text', 'value': '/compact'},
        ],
      }),
    );
    expect(
      [for (final k in back.barKeys) k.builtin ?? k.custom!.shown].take(4),
      ['image', 'paste', '/compact', 'Esc'],
    );
    expect([
      for (final k in AppSettings.decode(null).barKeys) k.builtin,
    ], builtinBarKeys);
  });

  // 立即发送是后来加的，旧设置读进来时被补在了最后、又存成了旧名字；改名后它回到 Enter 后面
  test('send-now saved under its old name goes back after Enter', () {
    final saved = [
      'image',
      'paste',
      'Esc',
      'Tab',
      'Enter',
      '⇧Tab',
      '^C',
      '↑',
      '↓',
      '←',
      '→',
      '立即发送',
    ];
    final keys = [
      for (final name in saved) ?BarKey.fromJson({'builtin': name}),
    ];
    final names = [for (final k in normalizeBarKeys(keys)) k.builtin];
    expect(names, [
      'image',
      'paste',
      'Esc',
      'Tab',
      'Enter',
      sendNowLabel,
      '⇧Tab',
      '^C',
      '清空',
      '退出',
      '↑',
      '↓',
      '←',
      '→',
    ]);
  });

  // 上一版读旧设置时把立即发送补在最后、又按新名字存了：没有版本号的设置读进来回到 Enter 后面；
  // 版本 2 之后自己挪到最后的不动
  test('send-now at the end of an unversioned bar goes back after Enter', () {
    List<String?> load(Object? version) {
      final names = [
        'image',
        'paste',
        'Esc',
        'Tab',
        'Enter',
        '⇧Tab',
        '^C',
        '↑',
        '↓',
        '←',
        '→',
        sendNowLabel,
      ];
      final s = AppSettings.decode(
        jsonEncode({
          'barKeys': [
            for (final n in names) {'builtin': n},
          ],
          'barKeysVersion': ?version,
        }),
      );
      return [for (final k in s.barKeys) k.builtin];
    }

    final old = load(null);
    expect(old.indexOf(sendNowLabel), old.indexOf('Enter') + 1);
    final moved = load(barKeysVersion);
    expect(moved.last, sendNowLabel);
    expect(AppSettings().toJson()['barKeysVersion'], barKeysVersion);
  });
}
