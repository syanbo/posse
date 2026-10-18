import 'models.dart';

/// 手机刚发出去、会话文件里还没有的一条话。
class LocalEcho {
  LocalEcho(this.text, this.images, this.sentAt);

  final String text;
  final int images;
  final DateTime sentAt;

  /// 终端已经收下；之前是"发送中"。
  var sent = false;
}

/// 发出去的话先画在对话末尾，agent 把它写进会话文件后换成正式的那条。
/// agent 收到的文字和手机上的不一定一样（图片换成了路径、排队的消息另有格式），
/// 所以不比内容：新出现的用户消息按顺序认领最早的一条。
class LocalEchoes {
  final _items = <LocalEcho>[];
  final _seen = <String>{};

  /// 两台机器的时钟可能差一点，早于发送时间这么多以内的也算。
  static const skew = Duration(seconds: 30);

  /// 一直没等到（比如 agent 不记这类输入）就不再挂着。
  static const expire = Duration(minutes: 5);

  List<LocalEcho> get items => List.unmodifiable(_items);

  bool get isEmpty => _items.isEmpty;

  LocalEcho add(String text, {int images = 0, DateTime? now}) {
    final echo = LocalEcho(text, images, now ?? DateTime.now());
    _items.add(echo);
    return echo;
  }

  void remove(LocalEcho echo) => _items.remove(echo);

  /// 消息窗口变了以后调用。返回有没有撤掉回显。
  bool reconcile(List<ChatMessage> messages, {DateTime? now}) {
    final at = now ?? DateTime.now();
    final before = _items.length;
    _items.removeWhere((e) => at.difference(e.sentAt) > expire);
    for (final message in messages) {
      if (message.role != 'user' || !_seen.add(message.id)) continue;
      if (_items.isEmpty) continue;
      final ts = message.timestamp;
      final oldest = _items.first.sentAt.subtract(skew);
      // 往上翻出来的旧消息不认领
      if (ts != null && ts < oldest.millisecondsSinceEpoch) continue;
      _items.removeAt(0);
    }
    return _items.length != before;
  }
}
