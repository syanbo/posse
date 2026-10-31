import 'dart:typed_data';

import 'package:libghostty/libghostty.dart';

/// Posse: 每个终端收到过几次清回滚（CSI 3J），渲染器看它变了就等这次重写画完。
/// libghostty 不报清回滚；回滚行数变少也不可靠，回滚满了裁掉旧页时同样会变少。
final scrollbackClears = Expando<int>('scrollbackClears');

/// 扫一批输出里的 CSI 3J，要在 [Terminal.write] 之前调：写入时同步通知渲染器。
/// 留着上一批末尾的三个字节，序列被切在两批之间也认得出。
class ScrollbackClearScanner {
  var _carry = const <int>[];

  void scan(Terminal terminal, Uint8List data) {
    final bytes = _carry.isEmpty ? data : [..._carry, ...data];
    var found = false;
    for (var i = 0; i + 3 < bytes.length; i++) {
      if (bytes[i] == 0x1b &&
          bytes[i + 1] == 0x5b &&
          bytes[i + 2] == 0x33 &&
          bytes[i + 3] == 0x4a) {
        found = true;
        break;
      }
    }
    final keep = bytes.length < 3 ? bytes.length : 3;
    _carry = bytes.sublist(bytes.length - keep);
    if (found) {
      scrollbackClears[terminal] = (scrollbackClears[terminal] ?? 0) + 1;
    }
  }
}
