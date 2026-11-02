import 'dart:ui';

import 'package:flutter_test/flutter_test.dart';
import 'package:flutter/widgets.dart';
import 'package:posse_mobile/chat/media.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/mermaid.dart';
import 'package:posse_mobile/usage/usage.dart';

/// 电脑发来的代号和英文源文本，手机按界面语言写出（现在只有中文，和电脑以前直接发的文字一致）。
void main() {
  test('media items without a name are labelled by their tag', () {
    MediaItem item(Map<String, Object?> j) =>
        MediaItem.fromJson({'id': 'x', 'kind': 'image', ...j});
    final zh = lookupAppLocalizations(const Locale('zh'));
    final en = lookupAppLocalizations(const Locale('en'));
    expect(item({'tag': 'generated', 'label': ''}).name(zh), '生成的图片');
    expect(item({'tag': 'codeOutput', 'label': ''}).name(zh), '代码产出');
    expect(item({'tag': 'pasted', 'label': ''}).name(zh), '粘贴');
    expect(item({'tag': 'pasted', 'label': ''}).name(en), 'Pasted');
    // 存到手机时用电脑给的名字，没有名字的按时间起名，不用界面语言的叫法
    expect(item({'tag': 'pasted', 'label': ''}).label, '');
    expect(item({'label': '#3'}).name(zh), '#3');
  });

  test('a plan’s monthly price says /月', () {
    final zh = lookupAppLocalizations(const Locale('zh'));
    final en = lookupAppLocalizations(const Locale('en'));
    expect(planLabel(zh, planName('Ultra \$200/mo')), 'Ultra \$200/月');
    expect(planLabel(en, planName('Ultra \$200/mo')), 'Ultra \$200/mo');
    expect(planName('plus'), 'Plus');
  });

  test('the Go library’s own diagram errors are translated', () {
    final zh = lookupAppLocalizations(const Locale('zh'));
    expect(mermaidErrorText(zh, 'drawing the diagram timed out'), '画图超时');
    expect(
      mermaidErrorText(zh, 'Parse error on line 2'),
      'Parse error on line 2',
    );
  });

  test('the phone tells the computer its language', () {
    expect(posseLocale(), 'zh-Hans');
  });
}
