import 'dart:ui' show Color;

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/terminal/term_colors.dart';

void main() {
  test('解析常驻进程给的终端配色', () {
    final palette = List.generate(
      16,
      (i) => '#0000${i.toRadixString(16).padLeft(2, '0')}',
    );
    final c = TermColors.parse({
      'foreground': '#ecebe8',
      'background': '#20201e',
      'palette': palette,
    });
    expect(c, isNotNull);
    expect(c!.background, const Color(0xFF20201E));
    expect(c.foreground, const Color(0xFFECEBE8));
    expect(c.palette[15], const Color(0xFF00000F));
    expect(
      c,
      TermColors.parse({
        'foreground': '#ecebe8',
        'background': '#20201e',
        'palette': [...palette],
      }),
    );
  });

  test('格式不对时不用', () {
    expect(TermColors.parse(null), isNull);
    expect(
      TermColors.parse({
        'foreground': '#fff',
        'background': '#000000',
        'palette': [],
      }),
      isNull,
    );
    expect(
      TermColors.parse({
        'foreground': '#ffffff',
        'background': '#000000',
        'palette': List.filled(15, '#000000'),
      }),
      isNull,
    );
  });
}
