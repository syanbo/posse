import 'dart:async';

/// 取消一个还没完成的握手。取消是协作的：尝试方要自己关掉套接字。
class CancelToken {
  bool _cancelled = false;
  bool get cancelled => _cancelled;

  void cancel() => _cancelled = true;
}

class RaceCancelled implements Exception {
  const RaceCancelled();
}

class RaceResult<T> {
  const RaceResult(this.url, this.value);
  final String url;
  final T value;
}

/// 并发握手，先成功的留下，其余取消。晚到的成功交给 [abandon]。
Future<RaceResult<T>> raceEndpoints<T>({
  required List<String> urls,
  required Future<T> Function(String url, CancelToken cancel) attempt,
  required Future<void> Function(T value) abandon,
}) {
  if (urls.isEmpty) {
    throw StateError('no endpoints');
  }
  final completer = Completer<RaceResult<T>>();
  final tokens = [for (final _ in urls) CancelToken()];
  var pending = urls.length;
  var won = false;
  Object? lastError;

  for (var i = 0; i < urls.length; i++) {
    final token = tokens[i];
    final url = urls[i];
    attempt(url, token)
        .then<void>((value) async {
          if (won || token.cancelled) {
            await abandon(value);
            return;
          }
          won = true;
          for (final other in tokens) {
            if (!identical(other, token)) other.cancel();
          }
          if (!completer.isCompleted) {
            completer.complete(RaceResult(url, value));
          }
        })
        .catchError((Object error) {
          lastError = error;
          pending--;
          if (!won && pending == 0 && !completer.isCompleted) {
            completer.completeError(
              lastError ?? StateError('all endpoints failed'),
            );
          }
        });
  }
  return completer.future;
}

/// 第一次签名只是当前网络，不算切换；签名变了才重连。
class NetworkGate {
  String? _last;

  bool shouldReconnect(String signature) {
    if (signature == _last) return false;
    final first = _last == null;
    _last = signature;
    return !first;
  }
}
