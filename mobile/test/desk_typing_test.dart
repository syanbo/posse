import 'dart:math';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/typing.dart';
import 'package:posse_mobile/desk/wire.dart';

/// Mac 上照收到的事件打字：退格删最后一个字，没字可删时记一次多删。
class _Mac {
  final runes = <int>[];
  var under = 0;

  void back() => runes.isEmpty ? under++ : runes.removeLast();
  void add(String s) => runes.addAll(s.runes);
  String get text => '${String.fromCharCodes(runes)}|$under';
}

/// 系统输入法：自己存着一份文字，每次编辑后把整个值交给框架；框架发来的重置晚 [lag] 次编辑才到，
/// 到之前的编辑都是在旧文字上做的。[echo] 为真时平台收下重置后把值再回送一次（有的平台会）。
class _Ime {
  _Ime({required this.lag, this.echo = false}) {
    typing = DeskTyping(now: () => clock);
  }

  final int lag;
  final bool echo;
  late final DeskTyping typing;
  var clock = Duration.zero;
  String platform = deskTypingPad;
  TextRange composing = TextRange.empty;
  final _sets = <int>[];
  var edits = 0;

  /// 平台收下的每次编辑，Mac 上应该得到的样子。
  final want = _Mac();

  /// 发给 Mac 的事件打出来的样子。
  final got = _Mac();

  bool get _composing => composing.isValid && !composing.isCollapsed;

  void _arrive() {
    while (_sets.isNotEmpty && edits - _sets.first >= lag) {
      _sets.removeAt(0);
      platform = deskTypingPad;
      // 组字被重置打断，这段没上屏
      composing = TextRange.empty;
      if (echo) _deliver();
    }
  }

  void _deliver() {
    final r = typing.take(
      TextEditingValue(
        text: platform,
        selection: TextSelection.collapsed(offset: platform.length),
        composing: composing,
      ),
    );
    _send(r);
    if (r.reset) {
      // 框架给输入框赋值，当场再进一次回调
      _send(
        typing.take(
          const TextEditingValue(
            text: deskTypingPad,
            selection: TextSelection.collapsed(offset: deskTypingPad.length),
          ),
        ),
      );
      _sets.add(edits);
    }
  }

  void _send(({int deleted, String added, bool reset}) r) {
    for (var i = 0; i < r.deleted; i++) {
      got.back();
    }
    got.add(r.added);
  }

  void _edit(void Function() change) {
    _arrive();
    change();
    edits++;
    clock += const Duration(milliseconds: 20);
    _deliver();
  }

  /// 打字或粘贴：一次插进 [s]。
  void type(String s) => _edit(() {
    platform += s;
    want.add(s);
  });

  void backspace() {
    _arrive();
    if (platform.isEmpty) {
      // 框里没字了，系统键盘按退格没反应
      edits++;
      return;
    }
    _edit(() {
      final r = platform.runes.toList()..removeLast();
      platform = String.fromCharCodes(r);
      want.back();
    });
  }

  /// 拼音组字：组字区换成 [partial]。
  void compose(String partial) => _edit(() {
    final start = _composing ? composing.start : platform.length;
    platform = platform.substring(0, start) + partial;
    composing = TextRange(start: start, end: platform.length);
  });

  /// 选了候选：组字区换成 [word] 上屏。
  void commit(String word) => _edit(() {
    final start = _composing ? composing.start : platform.length;
    platform = platform.substring(0, start) + word;
    composing = TextRange.empty;
    want.add(word);
  });

  /// 停一会儿：路上的重置都到了。
  void pause() {
    edits += lag + 1;
    clock += const Duration(seconds: 2);
    _arrive();
  }
}

void main() {
  // Codex 实测的样子：一次键入 169 个字，重置晚一个字才到，Mac 上收到的和打的一样。
  test('a long burst of typing is not repeated after the box resets', () {
    for (final lag in [1, 2, 3]) {
      final ime = _Ime(lag: lag);
      const typed =
          'qa14-abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz'
          'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz'
          'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcd-END';
      for (final ch in typed.split('')) {
        ime.type(ch);
      }
      ime.pause();
      expect(ime.got.text, '$typed|0', reason: 'lag $lag');
    }
  });

  // 一路退格删到垫字符快没了要重置，重置到之前又退格：不多删。
  test('backspaces around a reset are sent once each', () {
    for (final lag in [1, 2]) {
      final ime = _Ime(lag: lag);
      ime.type('hello world');
      for (var i = 0; i < 20; i++) {
        ime.backspace();
      }
      ime.type('ok');
      ime.pause();
      expect(ime.got.text, ime.want.text, reason: 'lag $lag');
    }
  });

  // 拼音组字跨过重置：上屏的字发一次。
  test('composing text across a reset', () {
    final ime = _Ime(lag: 1);
    ime.type('x' * 60);
    ime.compose('n');
    ime.compose('ni');
    ime.commit('你');
    ime.compose('h');
    ime.compose('hao');
    ime.commit('好');
    ime.type('y' * 70);
    ime.compose('s');
    ime.commit('是');
    ime.pause();
    expect(ime.got.text, ime.want.text);
  });

  // 隔一会儿再粘贴同样的一大段：照常发，不当成旧值。
  test('pasting the same long text again after a pause', () {
    final ime = _Ime(lag: 1);
    final a = 'paste-${'z' * 80}';
    ime.type(a);
    ime.pause();
    ime.type(a);
    ime.pause();
    ime.type(a);
    ime.type(a);
    ime.pause();
    expect(ime.got.text, '${a * 4}|0');
  });

  // 随机的打字、粘贴、中文和 emoji、退格、组字、停顿，重置晚 0 到 3 次编辑才到，平台收下后回不回送都试。
  test('random typing matches what the keyboard took', () {
    final r = Random(7);
    String piece(int n) => String.fromCharCodes([
      for (var i = 0; i < n; i++)
        switch (r.nextInt(5)) {
          0 => 0x4e00 + r.nextInt(500),
          1 => 0x1f600 + r.nextInt(40),
          _ => 0x61 + r.nextInt(26),
        },
    ]);
    for (var round = 0; round < 400; round++) {
      final ime = _Ime(lag: r.nextInt(4), echo: r.nextBool());
      for (var step = 0; step < 120; step++) {
        switch (r.nextInt(12)) {
          case 0:
            ime.type(piece(1 + r.nextInt(200)));
          case 1:
          case 2:
            for (var i = r.nextInt(8); i >= 0; i--) {
              ime.backspace();
            }
          case 3:
            ime.compose(piece(1));
            ime.compose(piece(2));
            ime.commit(piece(1 + r.nextInt(2)));
          case 4:
            if (r.nextInt(4) == 0) ime.pause();
          default:
            ime.type(piece(1));
        }
      }
      ime.pause();
      expect(
        ime.got.text,
        ime.want.text,
        reason: 'round $round lag ${ime.lag} echo ${ime.echo}',
      );
    }
  });
}
