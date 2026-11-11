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

/// 系统输入法：自己存着一份文字，每次编辑后把整个值交给框架；框架发来的重置晚 [lag] 次编辑才到
/// （为空时只在 [arrive] 时到），到之前的编辑都是在旧文字上做的。[echo] 为真时平台收下重置后把值再回送一次。
/// 框架这边照 EditableText：交来的值和框里一样就不往下交。
class _Ime {
  _Ime({this.lag, this.echo = false});

  final int? lag;
  final bool echo;
  final typing = DeskTyping();
  late TextEditingValue box = typing.padded;
  late String platform = box.text;
  TextRange composing = TextRange.empty;
  final _sets = <(int, String)>[];
  var edits = 0;

  /// 平台收下的每次编辑，Mac 上应该得到的样子。
  final want = _Mac();

  /// 发给 Mac 的事件打出来的样子。
  final got = _Mac();

  bool get _composing => composing.isValid && !composing.isCollapsed;
  bool get pending => _sets.isNotEmpty;

  void _due() {
    final lag = this.lag;
    while (lag != null && _sets.isNotEmpty && edits - _sets.first.$1 >= lag) {
      arrive();
    }
  }

  /// 最早的那个重置到了平台。
  void arrive() {
    platform = _sets.removeAt(0).$2;
    // 组字被重置打断，这段没上屏
    composing = TextRange.empty;
    if (echo) _deliver();
  }

  void _deliver() {
    final value = TextEditingValue(
      text: platform,
      selection: TextSelection.collapsed(offset: platform.length),
      composing: composing,
    );
    if (value == box) return;
    box = value;
    _take(value);
  }

  void _take(TextEditingValue value) {
    final r = typing.take(value);
    for (var i = 0; i < r.deleted; i++) {
      got.back();
    }
    got.add(r.added);
    if (r.reset case final reset?) {
      // 框架给输入框赋值，当场再进一次回调，再发给平台
      box = reset;
      _take(reset);
      _sets.add((edits, reset.text));
    }
  }

  void _edit(void Function() change) {
    _due();
    change();
    edits++;
    _deliver();
  }

  /// 打字或粘贴：一次插进 [s]。
  void type(String s) => _edit(() {
    platform += s;
    want.add(s);
  });

  void backspace() {
    _due();
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

  /// 全选删掉框里的正文，Mac 上删同样多的字。框里只有垫字符、或者有重置在路上时不做：回送的空值分不清
  /// 是删了哪一段，宁可少删（见 'select all while a reset is on the way deletes the least'）。
  void selectAllDelete() {
    _due();
    if (pending) return;
    final runes = platform.runes.toList();
    var pads = 0;
    while (pads < runes.length && _invisible.contains(runes[pads])) {
      pads++;
    }
    final n = runes.length - pads;
    if (n == 0) return;
    _edit(() {
      platform = '';
      for (var i = 0; i < n; i++) {
        want.back();
      }
    });
  }

  /// 输入法把开头的垫字符去掉（[to] 为空）或换成 [to] 那种字，同时打了 [s]。有重置在路上时只打字：
  /// 那时分不清是哪一段的垫字符被去掉，只保证少删（见 'values that start with neither pad'）。
  void normalize(String s, {int? to}) {
    _due();
    if (pending) return type(s);
    _normalize(s, to);
  }

  void _normalize(String s, int? to) => _edit(() {
    final runes = platform.runes.toList();
    var pads = 0;
    while (pads < runes.length && _invisible.contains(runes[pads])) {
      pads++;
    }
    final rest = String.fromCharCodes(runes.sublist(pads));
    platform =
        (to == null ? '' : String.fromCharCodes(List.filled(pads, to))) +
        rest +
        s;
    want.add(s);
  });

  /// 停一会儿：路上的重置都到了。
  void pause() {
    while (_sets.isNotEmpty) {
      arrive();
    }
  }

  /// 键盘收起再弹出：路上的重置丢了，新开的连接把框里的值交给平台。
  void reopen() {
    _sets.clear();
    platform = box.text;
    composing = TextRange.empty;
    if (typing.reopened(box.text) case final pad?) {
      box = pad;
      _take(pad);
      _sets.add((edits, pad.text));
    }
  }
}

const _invisible = [0x200B, 0x2060, 0x2063];

TextEditingValue _v(String s) => TextEditingValue(
  text: s,
  selection: TextSelection.collapsed(offset: s.length),
);

void main() {
  // Codex 实测的样子：一次键入 169 个字，重置晚几个字才到，Mac 上收到的和打的一样。
  test('a long burst of typing is not repeated after the box resets', () {
    for (final lag in [1, 2, 3, 10]) {
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

  // Codex 第 15 轮的反例：重置已被系统收下（之后的值开头是新的垫字符），隔多久再粘贴同样的 61 个字都照常发；
  // 新的一段只是开头和旧文字一样也照常发。系统收下重置后回送一次垫字符也一样。
  test('real input after the reset was taken is never swallowed', () {
    for (final confirm in [false, true]) {
      for (final text in ['q' * 61, 'a' * 60 + 'b']) {
        final typing = DeskTyping();
        final first = typing.take(_v(deskTypingPad + 'q' * 61));
        expect(first.added, 'q' * 61);
        final reset = first.reset!;
        expect(reset.text, isNot(deskTypingPad));
        // 框架给输入框赋值时当场回来的
        expect(typing.take(reset).added, '');
        if (confirm) expect(typing.take(reset).added, '');
        final next = typing.take(_v(reset.text + text));
        expect(next.deleted, 0, reason: '$confirm $text');
        expect(next.added, text, reason: '$confirm $text');
      }
    }
  });

  // 重置到了以后马上再粘贴：和旧文字一样、开头一样、更长、更短，路上那会儿先有几个旧文字上的编辑也一样。
  test('pasting right after the reset arrives, with late edits before it', () {
    final old = 'p' * 70;
    for (final late in [0, 1, 3]) {
      for (final paste in [
        old,
        old.substring(0, 30),
        '${old}tail',
        'p' * 69 + 'x',
        'z',
      ]) {
        final ime = _Ime();
        ime.type(old);
        expect(ime.pending, isTrue);
        for (var i = 0; i < late; i++) {
          ime.type('L');
        }
        ime.arrive();
        ime.type(paste);
        ime.pause();
        expect(
          ime.got.text,
          ime.want.text,
          reason: 'late $late paste ${paste.length}',
        );
      }
    }
  });

  // 迟到的编辑和重置到达交错：打字、退格、粘贴各种先后，平台回送和不回送。
  test('late edits interleaved with the reset arriving', () {
    final ops = <String, void Function(_Ime)>{
      'type': (i) => i.type('k'),
      'paste': (i) => i.type('m' * 80),
      'back': (i) => i.backspace(),
      'arrive': (i) {
        if (i.pending) i.arrive();
      },
    };
    final names = ops.keys.toList();
    // 四步的每种排法
    for (var code = 0; code < 256; code++) {
      for (final echo in [false, true]) {
        final ime = _Ime(echo: echo);
        ime.type('s' * 66);
        final seq = [for (var k = 0; k < 4; k++) names[(code >> (2 * k)) & 3]];
        for (final name in seq) {
          ops[name]!(ime);
        }
        ime.pause();
        ime.type('end');
        ime.pause();
        expect(ime.got.text, ime.want.text, reason: '$seq echo $echo');
      }
    }
  });

  // 两次重置紧挨着：重置一到就又攒满（或删到垫字符快没了）再重置。
  test('two resets back to back', () {
    for (final lag in [null, 1, 2]) {
      final ime = _Ime(lag: lag);
      ime.type('a' * 65);
      if (lag == null) ime.arrive();
      ime.type('b' * 65);
      ime.type('c');
      if (lag == null) ime.arrive();
      for (var i = 0; i < 6; i++) {
        ime.backspace();
      }
      if (lag == null) ime.arrive();
      ime.type('d' * 70);
      ime.pause();
      expect(ime.got.text, ime.want.text, reason: 'lag $lag');
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

  // 重置在路上时键盘收起再弹出：之后照常重置，不会一直攒着、垫字符删光。
  test('the keyboard reopening while a reset is on the way', () {
    final ime = _Ime();
    ime.type('r' * 70);
    ime.type('late');
    ime.reopen();
    ime.type('x');
    ime.arrive();
    ime.type('y' * 70);
    ime.pause();
    for (var i = 0; i < 80; i++) {
      ime.backspace();
      ime.pause();
    }
    expect(ime.got.text, ime.want.text);
    expect(ime.box.text.length, lessThanOrEqualTo(64));
  });

  // Codex 第 15 轮 generation_edges：重置已被系统收下（回送被 EditableText 挡掉）之后
  // 全选删除只删垫字符那么多；输入法把垫字符去掉或换成别的再打 Z，不删旧文字。
  test('values that start with neither pad', () {
    final t = DeskTyping();
    final a = t.take(_v('${t.padded.text}${'x' * 61}'));
    t.take(a.reset!);
    final got = t.take(_v(''));
    expect((got.deleted, got.added), (a.reset!.text.runes.length, ''));
    for (final normalized in ['', '\u200b' * 4, '\u2063' * 4]) {
      final t = DeskTyping();
      final a = t.take(_v('${t.padded.text}${'x' * 61}'));
      t.take(a.reset!);
      final got = t.take(_v('${normalized}Z'));
      expect(
        (got.deleted, got.added),
        (0, 'Z'),
        reason: normalized.runes.toString(),
      );
      // 之后照常：拉回一致，接着打字、退格都对
      final ime = _Ime();
      ime.type('x' * 61);
      ime.arrive();
      ime.normalize(
        'Z',
        to: normalized.isEmpty ? null : normalized.runes.first,
      );
      ime.pause();
      ime.type('next');
      for (var i = 0; i < 8; i++) {
        ime.backspace();
        ime.pause();
      }
      ime.type('end');
      ime.pause();
      expect(ime.got.text, ime.want.text, reason: normalized.runes.toString());
    }
  });

  // 重置还在路上时旧文字的垫字符被删得只剩一个、删光，或全选删了：不多删。
  test('late edits after the old pads are partly or fully gone', () {
    for (final deletes in [1, 2, 3, 4, 5]) {
      for (final selectAll in [false, true]) {
        final ime = _Ime();
        ime.type('ab');
        for (var i = 0; i < 2; i++) {
          ime.backspace();
        }
        // 垫字符剩不到两个时重置
        for (var i = 0; i < deletes; i++) {
          ime.backspace();
        }
        if (selectAll) ime.selectAllDelete();
        ime.type('q');
        ime.pause();
        ime.type('w');
        ime.pause();
        expect(ime.got.text, ime.want.text, reason: '$deletes $selectAll');
      }
    }
  });

  // 重置在路上时全选删掉：空值可能是旧文字删光了，也可能是新垫字符删光了，拿不准，按少的删，不多删。
  test('select all while a reset is on the way deletes the least', () {
    final t = DeskTyping();
    final a = t.take(_v('${t.padded.text}${'x' * 70}'));
    t.take(a.reset!);
    final got = t.take(_v(''));
    expect((got.deleted, got.added), (4, ''));
    // 之后照常
    final next = t.take(got.reset ?? _v(''));
    expect((next.deleted, next.added), (0, ''));
  });

  // 用户自己打的字开头就是这几种不可见字符，连着重置十次也照原样发。
  test('user text starting with the pad characters', () {
    for (final prefix in [
      '\u200b',
      '\u2060',
      '\u2063',
      '\u200b' * 4,
      '\u2060' * 4,
    ]) {
      final t = DeskTyping();
      var box = t.padded.text;
      for (var i = 0; i < 10; i++) {
        final text = '${prefix}batch$i-${'x' * 70}';
        final r = t.take(_v(box + text));
        expect((r.deleted, r.added), (0, text));
        t.take(r.reset!);
        box = r.reset!.text;
      }
    }
  });

  // 随机的打字、粘贴、中文和 emoji、退格、组字、停顿、重置后把框里的原样再打一遍，重置晚 0 到 3 次编辑才到，
  // 平台收下后回不回送都试。
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
      var last = '';
      for (var step = 0; step < 120; step++) {
        switch (r.nextInt(15)) {
          case 0:
            last = piece(1 + r.nextInt(200));
            ime.type(last);
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
          case 6:
            ime.selectAllDelete();
          case 7:
            ime.normalize(
              piece(1),
              to: null,
            );
          case 5:
            // 真的又打了一遍刚才的一段（或它的开头）
            if (last.isNotEmpty) {
              ime.type(
                r.nextBool() ? last : last.substring(0, last.length ~/ 2),
              );
            }
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
