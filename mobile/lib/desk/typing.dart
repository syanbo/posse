import 'package:flutter/services.dart';

import 'wire.dart';

/// 看桌面的隐藏输入框：从系统键盘交来的一个个编辑值里比出新打的字和删掉的字数。
///
/// 框里攒得多了、垫着的零宽空格快删完了，要把它重置成垫字符（见 [take]）。重置是框架发给系统输入法的，
/// 路上那一会儿系统还在旧文字上接着编辑、把旧文字加新字回送过来：拿重置后的垫字符去比，旧文字会再发一遍
/// （打快了重复前缀），退格会多删。所以重置后先记着重置前的文字，回送来的值离哪个近就接着哪个比。
class DeskTyping {
  DeskTyping({Duration Function()? now}) : _now = now ?? _clock();

  final Duration Function() _now;

  /// 上一个值。重置后是垫字符：系统收到重置后的编辑在它上面。
  String _shadow = deskTypingPad;

  /// 重置前的文字，系统还没收到重置时的编辑在它上面；之后每来一个旧值换成那个值。
  String? _stale;
  Duration _staleAt = Duration.zero;

  /// 重置到系统那边用不了这么久（同一台手机上一次平台消息来回）。过了还当旧值比的话，
  /// 隔一会儿再粘贴同样的一大段会被当成没变。
  static const _staleFor = Duration(seconds: 1);

  static Duration Function() _clock() {
    final watch = Stopwatch()..start();
    return () => watch.elapsed;
  }

  /// 输入框变成了 [value]：要发给电脑的退格数和文字，以及要不要把输入框重置成垫字符（[reset]）。
  ({int deleted, String added, bool reset}) take(TextEditingValue value) {
    const none = (deleted: 0, added: '', reset: false);
    // 组字还没上屏时先留着，动了输入框的话中文输入法会被打断。
    if (value.composing.isValid && !value.composing.isCollapsed) return none;
    final text = value.text;
    if (_stale != null && _now() - _staleAt > _staleFor) _stale = null;
    if (_stale case final old?) {
      // 正好是垫字符：自己重置时当场回来的，或者系统收下重置后回送的
      if (text == deskTypingPad) return none;
      if (_cost(old, text) < _cost(_shadow, text)) {
        // 系统还没收到重置时的编辑。不再重置：重置已经在路上，再发一次又多一段这样的空当
        final (deleted, added) = deskTypedDiff(old, text);
        _stale = text;
        _staleAt = _now();
        return (deleted: deleted, added: added, reset: false);
      }
      _stale = null;
    }
    final (deleted, added) = deskTypedDiff(_shadow, text);
    // 攒得多了、垫着的零宽空格快删完了才重置：每次都清空的话，上面那种旧值每个字都会来
    final reset =
        text.length > 64 || !text.startsWith(deskTypingPad.substring(2));
    if (reset) {
      _stale = text;
      _staleAt = _now();
      _shadow = deskTypingPad;
    } else {
      _shadow = text;
    }
    return (deleted: deleted, added: added, reset: reset);
  }

  /// 从 [a] 编辑成 [b] 要几下：删几个字加打几个字。
  static int _cost(String a, String b) {
    final (deleted, added) = deskTypedDiff(a, b);
    return deleted + added.runes.length;
  }
}
