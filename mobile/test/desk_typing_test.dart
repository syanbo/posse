import 'dart:math';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/typing.dart';
import 'package:posse_mobile/desk/wire.dart';

/// Mac 上照收到的事件打字：退格删最后一个字，没字可删时记一次多删。
class _Mac {
  final runes = <int>[];
  var under = 0;

  /// 一共删了几次（退格）。
  var backs = 0;

  void back() {
    backs++;
    runes.isEmpty ? under++ : runes.removeLast();
  }

  /// 一共打了几个字。
  var adds = 0;

  void add(String s) {
    adds += s.runes.length;
    runes.addAll(s.runes);
  }

  String get text => '${String.fromCharCodes(runes)}|$under';
}

/// 系统输入法：自己存着一份文字，每次编辑后把整个值交给框架；框架发来的重置晚 [lag] 次编辑才到
/// （为空时只在 [arrive] 时到），到之前的编辑都是在旧文字上做的。[echo] 为真时平台收下重置后把值再回送一次，
/// 给了 [echoes] 时每次到达随机回不回送。框架这边照 EditableText：交来的值和框里一样就不往下交。
///
/// [want] 是理想文档：用户在平台上的每次编辑直接作用在 Mac 上、从不重置（删掉垫字符也是删 Mac 上的字）；
/// [got] 是照发出的退格和文字打出来的 Mac 缓冲。
class _Ime {
  _Ime({this.lag, this.echo = false, this.echoes});

  final int? lag;
  final bool echo;
  final Random? echoes;

  /// 有重置在路上、或者 DeskTyping 还有分不清的旧段时，做过全选删除、一次删好几个字、去掉垫字符：
  /// 这些值对应两种可能，只保证不多删。
  var ambiguous = false;

  /// 为真时这几种操作只在状态清楚时做（不清楚时跳过）。
  var careful = false;
  var clock = Duration.zero;
  late final typing = DeskTyping(now: () => clock);
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
    if (echoes?.nextBool() ?? echo) _deliver();
  }

  bool get _unsure => pending || !typing.settled;

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
    clock += const Duration(milliseconds: 20);
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

  /// 全选删掉：框里的字（垫字符也算）都删掉，理想文档里删同样多的字。
  void selectAllDelete() => deleteMany(1 << 30);

  /// 一次删掉末尾 [n] 个字（选中一段删掉、按词删）。
  void deleteMany(int n) {
    _due();
    final runes = platform.runes.toList();
    final k = min(n, runes.length);
    if (k == 0) return;
    if (k > 1 && _unsure) {
      if (careful) return;
      ambiguous = true;
    }
    _edit(() {
      platform = String.fromCharCodes(runes.sublist(0, runes.length - k));
      for (var i = 0; i < k; i++) {
        want.back();
      }
    });
  }

  /// 输入法把开头的垫字符去掉（[to] 为空）或换成 [to] 那种字，同时打了 [s]（假想的输入法）。
  void normalize(String s, {int? to}) {
    _due();
    if (_unsure) {
      if (careful) return;
      ambiguous = true;
    }
    _edit(() {
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
  }

  /// 停一会儿：路上的重置都到了。
  void pause() {
    clock += const Duration(seconds: 2);
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

  // Codex 第 15 轮的六步最小反例：迟到 3 步、平台不回送、照 EditableText 过滤相同的值。
  // 原来三次重置同时在路上、垫字符轮完复用，多删。任何回送组合下都不能多删。
  test('codex six-step example never deletes more than the user did', () {
    for (var mask = 0; mask < 32; mask++) {
      final t = DeskTyping();
      var platform = t.padded.text, box = platform;
      var step = 0, arrivals = 0, user = 0, sent = 0;
      final queue = <(int, String)>[];
      void send(String s) {
        if (s == box) return;
        box = s;
        final a = t.take(_v(s));
        sent += a.deleted;
        if (a.reset case final r?) {
          queue.add((step + 3, r.text));
          box = r.text;
          t.take(r);
        }
      }

      String erase(String s, int n) {
        final a = s.runes.toList();
        return String.fromCharCodes(a.take(max(0, a.length - n)));
      }

      for (final op in [
        ('delete', 4),
        ('append', 0),
        ('clear', 0),
        ('append', 0),
        ('append', 0),
        ('delete', 3),
      ]) {
        step++;
        while (queue.isNotEmpty && queue.first.$1 <= step) {
          platform = queue.removeAt(0).$2;
          if ((mask & (1 << arrivals++)) != 0) send(platform);
        }
        switch (op.$1) {
          case 'append':
            platform += 'visible-literal';
          case 'delete':
            final n = min(platform.runes.length, op.$2);
            user += n;
            platform = erase(platform, n);
          default:
            user += platform.runes.length;
            platform = '';
        }
        send(platform);
        expect(sent, lessThanOrEqualTo(user), reason: 'mask $mask step $step');
      }
    }
  });

  // 端到端对照：用户的编辑直接作用在不重置的理想文档上，和照发出的事件打出来的 Mac 缓冲比。
  // 随机的打字、粘贴、中文和 emoji、退格、一次删好几个、全选删除（可以好几次）、组字、停顿、重置后把刚才那段
  // 再打一遍、输入法去掉或换掉垫字符；重置晚 0 到 5 次编辑才到，每次到达随机回不回送。
  // 每一步发出的退格都不多于用户删的。
  void editRandomly(
    int seed, {
    required bool careful,
    void Function(_Ime ime, int round)? done,
    void Function(String kind, int missed, int extra)? measure,
  }) {
    final r = Random(seed);
    String piece(int n) => String.fromCharCodes([
      for (var i = 0; i < n; i++)
        switch (r.nextInt(5)) {
          0 => 0x4e00 + r.nextInt(500),
          1 => 0x1f600 + r.nextInt(40),
          _ => 0x61 + r.nextInt(26),
        },
    ]);
    for (var round = 0; round < 600; round++) {
      final ime = _Ime(lag: r.nextInt(6), echoes: Random(round))
        ..careful = careful;
      var last = '';
      for (var step = 0; step < 120; step++) {
        final before = (
          ime.got.backs,
          ime.want.backs,
          ime.got.adds,
          ime.want.adds,
          ime.ambiguous,
        );
        var kind = '';
        switch (r.nextInt(16)) {
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
          case 5:
            if (last.isNotEmpty) {
              ime.type(
                r.nextBool() ? last : last.substring(0, last.length ~/ 2),
              );
            }
          case 6:
            kind = 'select all';
            ime.selectAllDelete();
          case 7:
            kind = 'delete several';
            ime.deleteMany(2 + r.nextInt(20));
          case 8:
            if (r.nextInt(3) == 0) {
              kind = 'pads removed or swapped';
              ime.normalize(
                piece(1),
                to: r.nextBool() ? null : _invisible[r.nextInt(3)],
              );
            }
          default:
            ime.type(piece(1));
        }
        expect(
          ime.got.backs,
          lessThanOrEqualTo(ime.want.backs),
          reason: 'round $round step $step: deleted more than the user did',
        );
        if (ime.ambiguous && !before.$5 && kind.isNotEmpty) {
          measure?.call(
            kind,
            (ime.want.backs - before.$2) - (ime.got.backs - before.$1),
            (ime.got.adds - before.$3) - (ime.want.adds - before.$4),
          );
        }
      }
      ime.pause();
      done?.call(ime, round);
    }
  }

  // 分不清的操作只在状态清楚时做：每一轮最后都和理想文档完全一样。
  test('random editing: the Mac buffer matches the ideal document', () {
    var exact = 0;
    editRandomly(
      7,
      careful: true,
      done: (ime, round) {
        if (ime.ambiguous) return;
        exact++;
        expect(
          ime.got.text,
          ime.want.text,
          reason: 'round $round lag ${ime.lag}',
        );
      },
    );
    expect(exact, 600);
  });

  // 什么时候都可能做分不清的操作：照样不多删；每种第一次碰上时少删、多打了几个字记下来（报告里的实测上界）。
  test('random editing with ambiguous edits never deletes more', () {
    final worst = <String, (int, int)>{};
    var rounds = 0;
    editRandomly(
      11,
      careful: false,
      done: (ime, round) {
        if (ime.ambiguous) rounds++;
      },
      measure: (kind, missed, extra) {
        final (m, e) = worst[kind] ?? (0, 0);
        worst[kind] = (max(m, missed), max(e, extra));
      },
    );
    // ignore: avoid_print
    print(
      'ambiguous rounds $rounds, worst (missed deletions, extra typed) per kind: $worst',
    );
  });
}
