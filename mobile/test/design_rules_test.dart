import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

// 界面取值只用 lib/ui/theme.dart 里的名字（Fs、Fw、Lh、Ic、Rd、PosseColors），间距只用阶梯上的数，
// 见 docs/mobile-design.md；桌面端由 internal/app/fonts_test.go 检查同样的规则。

const spaceScale = [0, 2, 4, 6, 8, 10, 12, 14, 16, 20, 24, 32];

// 自己定义颜色的地方：主题、语法高亮的配色、原生闪屏共用的底色和标志色。
const colorFiles = {'ui/theme.dart', 'ui/highlight.dart', 'ui/splash.dart'};

class _Source {
  _Source(this.path, this.text);
  final String path; // 相对 lib/
  final String text;

  Iterable<(int, String)> lines() sync* {
    final all = text.split('\n');
    for (var i = 0; i < all.length; i++) {
      yield (i + 1, all[i]);
    }
  }

  int lineOf(int offset) =>
      '\n'.allMatches(text.substring(0, offset)).length + 1;
}

List<_Source> _sources() {
  final root = Directory('lib');
  return [
    for (final f in root.listSync(recursive: true).whereType<File>())
      if (f.path.endsWith('.dart'))
        _Source(f.path.substring('lib/'.length), f.readAsStringSync()),
  ]..sort((a, b) => a.path.compareTo(b.path));
}

void _report(List<String> errors, String hint) {
  if (errors.isNotEmpty) fail('$hint：\n${errors.join('\n')}');
}

void main() {
  final sources = _sources();

  test('字号、字重、行高、圆角、图标尺寸不写数字', () {
    final bad = RegExp(
      r'fontSize:\s*[0-9]|\bmono\(\s*[0-9]|FontWeight\.w[0-9]|FontWeight\.bold|'
      r'\bheight:\s*1\.[0-9]|circular\(\s*[0-9]|\b(icon)?[sS]ize:\s*[0-9]',
    );
    final errors = <String>[
      for (final s in sources)
        if (s.path != 'ui/theme.dart')
          for (final (n, line) in s.lines())
            if (bad.hasMatch(line)) '${s.path}:$n ${line.trim()}',
    ];
    _report(errors, '用 theme.dart 里的 Fs、Fw、Lh、Rd、Ic');
  });

  test('间距在阶梯上', () {
    final call = RegExp(r'EdgeInsets(?:Directional)?\.\w+\(([^()]*)\)');
    final spacer = RegExp(
      r'SizedBox\(\s*(?:width|height):\s*([0-9.]+),?\s*\)|\b(?:run)?[sS]pacing:\s*([0-9.]+)',
    );
    final num = RegExp(r'(?:^|[^\w.])([0-9]+(?:\.[0-9]+)?)\b');
    final errors = <String>[];
    for (final s in sources) {
      for (final m in call.allMatches(s.text)) {
        for (final n in num.allMatches(m.group(1)!)) {
          if (!spaceScale.contains(double.parse(n.group(1)!))) {
            errors.add('${s.path}:${s.lineOf(m.start)} ${m.group(0)}');
          }
        }
      }
      for (final m in spacer.allMatches(s.text)) {
        final v = double.parse((m.group(1) ?? m.group(2))!);
        if (!spaceScale.contains(v)) {
          errors.add('${s.path}:${s.lineOf(m.start)} ${m.group(0)}');
        }
      }
    }
    _report(errors, '间距只用 $spaceScale');
  });

  test('颜色只在主题里定义', () {
    final bad = RegExp(
      r'Color\(0x|Colors\.(?:black|white)[0-9]+|Colors\.(?:black|white)\.withValues|'
      r'\bColors\.(?!white\b|black\b|transparent\b)[a-z]+\b',
    );
    final errors = <String>[
      for (final s in sources)
        if (!colorFiles.contains(s.path))
          for (final (n, line) in s.lines())
            if (bad.hasMatch(line)) '${s.path}:$n ${line.trim()}',
    ];
    _report(errors, '用 PosseColors 的名字、Brand、DeskColors');
  });

  test('Material 图标统一用 Rounded', () {
    final icon = RegExp(r'\bIcons\.([a-z0-9_]+)');
    final errors = <String>[
      for (final s in sources)
        for (final (n, line) in s.lines())
          for (final m in icon.allMatches(line))
            if (!m.group(1)!.endsWith('_rounded')) '${s.path}:$n ${m.group(0)}',
    ];
    _report(errors, 'Outlined、Filled 换成对应的 _rounded');
  });
}
