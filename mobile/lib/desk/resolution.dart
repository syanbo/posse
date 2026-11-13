import 'dart:async';

import '../app_state.dart';

int? deskResolutionOffer(bool supported, AppSettings s, String? key) =>
    supported ? s.deskResolution[key] ?? 0 : null;

class _ResolutionRequest {
  _ResolutionRequest(this.generation, this.value, this.call);

  final int generation;
  final int value;
  final Future<Object?> Function(String, Map<String, Object?>) call;
  final done = Completer<void>();
}

/// 一个请求在途，尚未发出的选择只留最新值；偏好仍在点击时立即保存。
class DeskResolutionChoice {
  _ResolutionRequest? _pending;
  bool _sending = false;
  bool _disposed = false;
  int _generation = 0;

  Future<void> choose(
    PosseAppModel model,
    String? key,
    int resolution, {
    Future<Object?> Function(String method, Map<String, Object?> params)? call,
  }) {
    if (_disposed) return Future.value();
    if (!deskResolutionChoices.contains(resolution)) resolution = 0;
    if (key != null) {
      model.updateSettings((s) {
        if (resolution == 0) {
          s.deskResolution.remove(key);
        } else {
          s.deskResolution[key] = resolution;
        }
      });
    }
    _pending?.done.complete();
    _pending = null;
    if (call == null) return Future.value();
    final request = _ResolutionRequest(_generation, resolution, call);
    _pending = request;
    if (!_sending) unawaited(_drain());
    return request.done.future;
  }

  Future<void> _drain() async {
    _sending = true;
    while (true) {
      final request = _pending;
      if (request == null) break;
      _pending = null;
      try {
        await request.call('desk.resolution', {'resolution': request.value});
        request.done.complete();
      } on Object catch (error, stack) {
        if (request.generation == _generation) {
          request.done.completeError(error, stack);
        } else {
          request.done.complete();
        }
      }
    }
    _sending = false;
  }

  /// 换观看会话时丢掉未发送的旧请求，旧在途请求的错误也不再交给新会话。
  void cancelPending() {
    _generation++;
    _pending?.done.complete();
    _pending = null;
  }

  void dispose() {
    _disposed = true;
    cancelPending();
  }
}
