import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

// 界面文字经 lib/l10n 的 ARB（gen-l10n）：英文表 app_en.arb 是模板，中文表 app_zh.arb 键要一样全；
// lib 里不写中文字面量，还没改完的文件列在 l10nPending。

/// 界面文字还没换成 ARB 键的文件（相对 lib/），换完一个去掉一个。
const l10nPending = {
  'chat/transcript.dart',
  'history/models.dart',
  'usage/usage.dart',
};

/// 允许中文的：生成的中文表，和各语言用自己名字的语言列表。
const allowed = {'l10n/app_localizations_zh.dart', 'l10n/l10n.dart'};

Set<String> _keys(String name) {
  final map = jsonDecode(File('lib/l10n/$name').readAsStringSync()) as Map;
  return {
    for (final k in map.keys.cast<String>())
      if (!k.startsWith('@')) k,
  };
}

final _hanString = RegExp(r'''(['"])[^'"\n]*\p{Script=Han}''', unicode: true);

/// 去掉注释后含汉字字符串的行号。
List<int> _hanLines(String text) {
  final out = <int>[];
  final lines = text.split('\n');
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    if (line.trimLeft().startsWith('//')) continue;
    final comment = line.indexOf(' // ');
    if (comment >= 0) line = line.substring(0, comment);
    if (_hanString.hasMatch(line)) out.add(i + 1);
  }
  return out;
}

void main() {
  test('中文表和英文表的键一样', () {
    final en = _keys('app_en.arb');
    final zh = _keys('app_zh.arb');
    expect(en.difference(zh), isEmpty, reason: 'app_zh.arb 缺的键');
    expect(zh.difference(en), isEmpty, reason: 'app_zh.arb 多的键');
  });

  test('lib 里不写中文字面量', () {
    final errors = <String>[];
    final seen = <String>{};
    for (final f in Directory(
      'lib',
    ).listSync(recursive: true).whereType<File>()) {
      if (!f.path.endsWith('.dart')) continue;
      final path = f.path.substring('lib/'.length);
      if (allowed.contains(path)) continue;
      final lines = _hanLines(f.readAsStringSync());
      if (l10nPending.contains(path)) {
        seen.add(path);
        if (lines.isEmpty) errors.add('$path 已经没有中文字面量，从 l10nPending 里去掉');
      } else {
        for (final n in lines) {
          errors.add('$path:$n 界面文字要经 lib/l10n 的 ARB 键');
        }
      }
    }
    for (final p in l10nPending.difference(seen)) {
      errors.add('l10nPending 里的 $p 不存在了');
    }
    if (errors.isNotEmpty) fail(errors.join('\n'));
  });
}
