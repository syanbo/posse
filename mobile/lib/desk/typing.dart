import 'package:flutter/services.dart';

import 'wire.dart';

/// 垫字符轮流用的三种字：零宽空格（[deskTypingPad]）、WORD JOINER、INVISIBLE SEPARATOR。
/// 都看不见，各自成一个字（不和前后的字连成一个），退格一次删一个。
const _padChars = [0x200B, 0x2060, 0x2063];

String _padOf(int char) => String.fromCharCodes(List.filled(4, char));

int _lead(String s) => s.isEmpty ? -1 : s.runes.first;

/// 一段：系统输入框里一路编辑下来的文字。[char] 是开这一段的重置用的垫字符（-1 是键盘重开时框里原有的），
/// [base] 是这一段最后一个值。
class _Chain {
  _Chain(this.char, [String? base]) : base = base ?? _padOf(char);
  final int char;
  String base;
}

/// 看桌面的隐藏输入框：从系统键盘交来的一个个编辑值里比出新打的字和删掉的字数。
///
/// 框里攒得多了、垫字符快删完了，要把它重置成垫字符。重置是框架发给系统输入法的，路上那一会儿系统还在
/// 旧文字上接着编辑、把旧文字加新字回送过来，拿重置后的垫字符去比的话旧文字会再发一遍、退格会多删。
/// 系统收下重置后不一定回送；回送了也和框里的值一样，EditableText 不往下交。所以每次重置换一种垫字符，
/// 每次重置开一段：
///
/// - 开头是最新这种垫字符的值，是系统收下最近一次重置以后的，接着它比；
/// - 别的值（开头是更早的垫字符：路上那会儿在旧文字上的编辑；都不是：全选删掉了、输入法把垫字符去掉或
///   换了）分不清接着哪段，和各段最后的值比一比，取改动最少的（删的加打的，开头的字变了也算一次：
///   一次按键的改动本来就小）；一样少取开头同类的（同一种垫字符，或者都没有），再取删得少的：往 Mac
///   多删比少删糟。开头是第 k 段垫字符的，不和更早的段比：系统按顺序收下重置，更早的段里没有这种字。
///
/// 值接着哪段比就记在哪段上，更早的段作废。接着最新一段比的值、没有垫字符的值才会触发重置；新的垫字符
/// 要和各段的、各段现在开头的字都不同，没有就先不重置，等系统收下了再说。
class DeskTyping {
  final _chains = [_Chain(_padChars[0])];

  /// 刚重置、框架给输入框赋值时当场回来的那一次。
  var _echo = false;

  _Chain get _current => _chains.last;

  /// 现在的垫字符：输入框一开始和每次重置都设成它。
  TextEditingValue get padded {
    final pad = _padOf(_current.char);
    return TextEditingValue(
      text: pad,
      selection: TextSelection.collapsed(offset: pad.length),
    );
  }

  /// 输入框变成了 [value]：要发给电脑的退格数和文字；[reset] 不为空时把输入框设成它。
  ({int deleted, String added, TextEditingValue? reset}) take(
    TextEditingValue value,
  ) {
    const none = (deleted: 0, added: '', reset: null);
    // 组字还没上屏时先留着，动了输入框的话中文输入法会被打断。
    if (value.composing.isValid && !value.composing.isCollapsed) return none;
    final text = value.text;
    if (_echo && text == _padOf(_current.char)) {
      _echo = false;
      return none;
    }
    _echo = false;
    final lead = _lead(text);
    var pick = _chains.length - 1;
    late (int, String) diff;
    if (lead == _current.char) {
      // 系统已经收下最近一次重置
      diff = deskTypedDiff(_baseOf(_current), text);
    } else {
      // 开头的字不同（垫字符被换掉、去掉）本身也算一次改动
      (int, int, int) rank((int, String) d, String base) => (
        d.$1 + d.$2.runes.length + (_lead(base) == lead ? 0 : 1),
        _kind(base) == _kind(text) ? 0 : 1,
        d.$1,
      );
      var base = _baseFor(_current, lead);
      diff = _diff(base, text);
      var best = rank(diff, base);
      // 开头是第 k 段的垫字符：系统收下了第 k 段的重置，比它更早的段里没有这种字，不用比
      final from = _chains.indexWhere((c) => c.char == lead);
      for (var i = _chains.length - 2; i >= (from < 0 ? 0 : from); i--) {
        base = _baseFor(_chains[i], lead);
        final d = _diff(base, text);
        final r = rank(d, base);
        if (r.$1 < best.$1 ||
            r.$1 == best.$1 &&
                (r.$2 < best.$2 || r.$2 == best.$2 && r.$3 < best.$3)) {
          pick = i;
          diff = d;
          best = r;
        }
      }
    }
    // 记在接着比的那段上。系统按顺序收下重置，走到了这一段，更早的段不会再有值
    final chain = _chains[pick];
    chain.base = text;
    _chains.removeRange(0, pick);
    final (deleted, added) = diff;
    // 攒得多了、垫字符剩不到两个才重置：每次都清空的话路上那会儿的编辑每个字都会有。
    // 没有垫字符的值接着哪段都可能（系统其实已经收下了更新的重置、垫字符又被去掉了），也重置：
    // 新的垫字符和各段都不同，多一个在路上的重置分得开
    final pad = _padOf(_current.char);
    final free = _freeChar();
    if ((identical(chain, _current) || _kind(text) == -1) &&
        free != null &&
        (text.length > 64 || !text.startsWith(pad.substring(2)))) {
      _chains.add(_Chain(free));
      _echo = true;
      return (deleted: deleted, added: added, reset: padded);
    }
    return (deleted: deleted, added: added, reset: null);
  }

  /// 键盘重新弹出来：输入连接是新开的，系统拿到的是框里现在的 [text]。之前的重置还没被收下的话
  /// （键盘收起时正好在路上），框里留着旧文字，以后可能一直没有最新一段的值、再也不重置：从它重来一次。
  TextEditingValue? reopened(String text) {
    if (_chains.length == 1 && _lead(text) == _current.char) return null;
    _chains
      ..clear()
      ..add(_Chain(-1, text));
    _chains.add(_Chain(_freeChar()!));
    _echo = true;
    return padded;
  }

  /// 接着 [chain] 比的基准。值的开头是这段的垫字符、记着的值却不是：记着的那些是更早文字上的
  /// （分不清时记到了这段），系统刚收下这段的重置，接着它的垫字符比。
  String _baseOf(_Chain chain) =>
      _lead(chain.base) == chain.char ? chain.base : _padOf(chain.char);

  String _baseFor(_Chain chain, int lead) =>
      lead == chain.char ? _baseOf(chain) : chain.base;

  /// 还没用着的垫字符：各段的、各段现在开头的都不能用，否则分不清；没有就是 null。
  int? _freeChar() {
    for (final c in _padChars) {
      if (!_chains.any((chain) => chain.char == c || _lead(chain.base) == c)) {
        return c;
      }
    }
    return null;
  }
}

/// 从 [before] 编辑成 [after]。开头是同一种字时逐字比（用户自己打的字开头也可能是这几种字）；
/// 不是时垫字符和正文分开：正文变了只算正文的差（垫字符被输入法去掉、换掉不算退格），正文没变时
/// 垫字符少了几个就是几次退格。
(int, String) _diff(String before, String after) {
  if (_lead(before) == _lead(after)) return deskTypedDiff(before, after);
  final (padsBefore, textBefore) = _splitPads(before);
  final (padsAfter, textAfter) = _splitPads(after);
  final (deleted, added) = deskTypedDiff(textBefore, textAfter);
  if (deleted > 0 || added.isNotEmpty) return (deleted, added);
  return (padsBefore > padsAfter ? padsBefore - padsAfter : 0, '');
}

/// 开头是哪种垫字符，不是垫字符的都算一类（-1）。
int _kind(String s) {
  final lead = _lead(s);
  return _padChars.contains(lead) ? lead : -1;
}

/// 开头连着几个垫字符（三种都算）和后面的正文。
(int, String) _splitPads(String s) {
  final runes = s.runes.toList();
  var n = 0;
  while (n < runes.length && _padChars.contains(runes[n])) {
    n++;
  }
  return (n, String.fromCharCodes(runes.sublist(n)));
}
