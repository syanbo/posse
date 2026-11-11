import 'package:flutter/services.dart';

import 'wire.dart';

/// 两代垫字符轮流用：零宽空格（[deskTypingPad]）和 WORD JOINER。都看不见，各自成一个字，退格一次删一个。
const _pads = [deskTypingPad, '⁠⁠⁠⁠'];

/// 看桌面的隐藏输入框：从系统键盘交来的一个个编辑值里比出新打的字和删掉的字数。
///
/// 框里攒得多了、垫字符快删完了，要把它重置成垫字符。重置是框架发给系统输入法的，路上那一会儿系统还在
/// 旧文字上接着编辑、把旧文字加新字回送过来，拿重置后的垫字符去比的话旧文字会再发一遍、退格会多删。
/// 平台收下重置后不一定回送；回送了也和框里的值一样，EditableText 不往下交。所以每次重置换一种垫字符：
/// 开头是这一代垫字符的值是系统收下重置以后的，接着重置后的垫字符比；不是的是路上那会儿在旧文字上的编辑，
/// 接着旧文字比。看到这一代的值（系统已经收下）之前不再重置，路上最多一个重置，两种垫字符轮流就分得开。
class DeskTyping {
  var _gen = 0;

  /// 这一代的上一个值，系统收下重置后的编辑都接着它比。
  String _shadow = deskTypingPad;

  /// 重置了、系统还没收下：路上那会儿系统在这段旧文字上编辑，每来一个这样的值就换成它。
  String? _stale;

  /// 刚重置、框架给输入框赋值时当场回来的那一次。
  var _echo = false;

  /// 现在这一代的垫字符：输入框一开始和每次重置都设成它。
  TextEditingValue get padded => TextEditingValue(
    text: _pad,
    selection: TextSelection.collapsed(offset: _pad.length),
  );

  String get _pad => _pads[_gen % 2];

  /// 输入框变成了 [value]：要发给电脑的退格数和文字；[reset] 不为空时把输入框设成它。
  ({int deleted, String added, TextEditingValue? reset}) take(
    TextEditingValue value,
  ) {
    const none = (deleted: 0, added: '', reset: null);
    // 组字还没上屏时先留着，动了输入框的话中文输入法会被打断。
    if (value.composing.isValid && !value.composing.isCollapsed) return none;
    final text = value.text;
    if (_echo && text == _pad) {
      _echo = false;
      return none;
    }
    _echo = false;
    if (_stale case final old? when !text.startsWith(_pad[0])) {
      final (deleted, added) = deskTypedDiff(old, text);
      _stale = text;
      return (deleted: deleted, added: added, reset: null);
    }
    _stale = null;
    final (deleted, added) = deskTypedDiff(_shadow, text);
    // 攒得多了、垫字符剩不到两个才重置：每次都清空的话路上那会儿的编辑每个字都会有
    if (text.length > 64 || !text.startsWith(_pad.substring(2))) {
      _stale = text;
      _gen++;
      _shadow = _pad;
      _echo = true;
      return (deleted: deleted, added: added, reset: padded);
    }
    _shadow = text;
    return (deleted: deleted, added: added, reset: null);
  }

  /// 键盘重新弹出来：输入连接是新开的，系统拿到的是框里现在的 [text]。之前的重置还没被收下的话
  /// （键盘收起时正好在路上），框里留着旧文字，以后的值都不会是这一代的，再也不重置：照这一代再设一次。
  TextEditingValue? reopened(String text) {
    if (_stale == null || text.startsWith(_pad[0])) return null;
    _stale = text;
    _echo = true;
    return padded;
  }
}
