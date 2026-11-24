import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:posse_quic/posse_quic.dart';

/// 手机端内置 Tailscale 的底层接口，默认由 [NativeTailscaleBackend] 经 FFI 与原生通道实现，
/// 测试中可替换为内存桩避免加载动态库。
abstract class TailscaleBackend {
  Stream<TailscaleStatus> get updates;
  TailscaleStatus status();
  Future<String> stateDir();
  Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  });
  Future<TailscaleStatus> stop();
  Future<TailscaleResetResult> reset({String stateDir = ''});
  Future<TailscaleStatus> startLogin();
  void wake();
  Future<TailscalePeerRoute> peerRoute(String ip);
  Future<TailscaleUdpBridge> openUdpBridge(String host, int port);
}

class NativeTailscaleBackend implements TailscaleBackend {
  const NativeTailscaleBackend({MethodChannel? channel})
    : _channel = channel ?? const MethodChannel('posse/tailscale');

  final MethodChannel _channel;

  @override
  Stream<TailscaleStatus> get updates => TailscaleNode.updates;

  @override
  TailscaleStatus status() => TailscaleNode.status();

  @override
  Future<String> stateDir() async {
    try {
      final dir = await _channel.invokeMethod<String>('stateDir');
      if (dir == null || dir.isEmpty) {
        throw TailscaleException('state directory unavailable');
      }
      return dir;
    } on PlatformException catch (e) {
      final msg = e.message?.trim() ?? '';
      throw TailscaleException(
        msg.isNotEmpty ? msg : 'state directory unavailable',
      );
    }
  }

  @override
  Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) => TailscaleNode.start(
    stateDir: stateDir,
    hostname: hostname,
    controlUrl: controlUrl,
    authKey: authKey,
    disableBindInterface: disableBindInterface,
  );

  @override
  Future<TailscaleStatus> stop() => TailscaleNode.stop();

  @override
  Future<TailscaleResetResult> reset({String stateDir = ''}) =>
      TailscaleNode.reset(stateDir: stateDir);

  @override
  Future<TailscaleStatus> startLogin() => TailscaleNode.startLogin();

  @override
  void wake() => TailscaleNode.wake();

  @override
  Future<TailscalePeerRoute> peerRoute(String ip) =>
      TailscaleNode.peerRoute(ip);

  @override
  Future<TailscaleUdpBridge> openUdpBridge(String host, int port) =>
      TailscaleNode.openUdpBridge(host, port);
}

bool get tailscaleSupported => Platform.isIOS || Platform.isAndroid;

/// 管理手机进程级内置 Tailscale 节点：默认不启动（零内存/网络开销），开启后按需懒启动，
/// 订阅状态变化并缓存对端路由查询（Direct / PeerRelay / DERP）。
class TailscaleController extends ChangeNotifier {
  TailscaleController({this._backend});

  final TailscaleBackend? _backend;
  TailscaleStatus _status = const TailscaleStatus();
  StreamSubscription<TailscaleStatus>? _sub;
  String? _cachedStateDir;
  int _pendingOps = 0;
  int _opSeq = 0;
  Future<void> _opChain = Future<void>.value();
  String? _error;
  bool _disposed = false;

  int _sessionGen = 0;
  int _routeCacheEpoch = 0;
  final _routeCache = <String, (TailscalePeerRoute, DateTime)>{};
  static const _routeCacheTtl = Duration(seconds: 2);

  /// 节点从非 Running 变为 Running 时触发，供 [PosseAppModel] 尝试尽快接上电脑。
  void Function()? onBecameRunning;

  bool get supported => _backend != null;
  TailscaleStatus get status => _status;
  bool get isRunning => _status.isRunning;
  bool get busy => _pendingOps > 0;
  bool get hasStartedOrQueued => _opSeq > 0;
  String? get error => _error;

  /// 每次节点停止、重置或离开 Running 时递增，供看桌面识别底层已关闭的回环 UDP 桥避免误复用。
  int get sessionGen => _sessionGen;

  void _invalidateRouteCache() {
    _routeCacheEpoch++;
    _routeCache.clear();
  }

  void _ensureSubscribed() {
    final backend = _backend;
    if (backend == null || _sub != null) return;
    _sub = backend.updates.listen(_applyStatus);
  }

  void _applyStatus(TailscaleStatus next) {
    if (_disposed) return;
    final wasRunning = _status.isRunning;
    _status = next;
    if (wasRunning && !next.isRunning) {
      _sessionGen++;
    }
    if (!next.isRunning) {
      _invalidateRouteCache();
    }
    notifyListeners();
    if (!wasRunning && next.isRunning) {
      onBecameRunning?.call();
    }
  }

  Future<String> _stateDir() async {
    final cached = _cachedStateDir;
    if (cached != null && cached.isNotEmpty) return cached;
    final backend = _backend;
    if (backend == null) {
      throw TailscaleException('Tailscale is not available on this platform');
    }
    final dir = await backend.stateDir();
    _cachedStateDir = dir;
    return dir;
  }

  Future<T> _runSerialized<T>(
    int seq,
    Future<T> Function() action, {
    required T Function() onSuperseded,
  }) {
    _pendingOps++;
    _error = null;
    if (!_disposed) notifyListeners();
    final completer = Completer<T>();
    _opChain = _opChain.then((_) async {
      try {
        if (_disposed || seq != _opSeq) {
          completer.complete(onSuperseded());
          return;
        }
        final res = await action();
        completer.complete(res);
      } catch (e, st) {
        _error = e is TailscaleException ? e.message : '$e';
        completer.completeError(e, st);
      } finally {
        _pendingOps--;
        if (!_disposed) notifyListeners();
      }
    });
    return completer.future;
  }

  Future<bool> start({
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) {
    final backend = _backend;
    if (backend == null) return Future<bool>.value(false);
    _ensureSubscribed();
    final seq = ++_opSeq;
    return _runSerialized<bool>(seq, () async {
      final dir = await _stateDir();
      if (_disposed || seq != _opSeq) return false;
      final st = await backend.start(
        stateDir: dir,
        hostname: hostname,
        controlUrl: controlUrl,
        authKey: authKey,
        disableBindInterface: disableBindInterface,
      );
      _applyStatus(st);
      return true;
    }, onSuperseded: () => false);
  }

  Future<bool> stop() {
    final backend = _backend;
    if (backend == null) return Future<bool>.value(false);
    final seq = ++_opSeq;
    return _runSerialized<bool>(seq, () async {
      _sessionGen++;
      _invalidateRouteCache();
      final st = await backend.stop();
      _applyStatus(st);
      return true;
    }, onSuperseded: () => false);
  }

  Future<TailscaleResetResult?> reset() {
    final backend = _backend;
    if (backend == null) return Future<TailscaleResetResult?>.value();
    _ensureSubscribed();
    final seq = ++_opSeq;
    return _runSerialized<TailscaleResetResult?>(seq, () async {
      final dir = await _stateDir();
      if (_disposed || seq != _opSeq) return null;
      _sessionGen++;
      _invalidateRouteCache();
      final res = await backend.reset(stateDir: dir);
      _applyStatus(res.status);
      return res;
    }, onSuperseded: () => null);
  }

  Future<void> startLogin() {
    final backend = _backend;
    if (backend == null) return Future<void>.value();
    _ensureSubscribed();
    final seq = ++_opSeq;
    return _runSerialized<void>(seq, () async {
      final st = await backend.startLogin();
      _applyStatus(st);
    }, onSuperseded: () {});
  }

  void wake() {
    _invalidateRouteCache();
    _backend?.wake();
  }

  /// 查询到指定 100.x 对端的实际路径（短缓存 2 秒，wake 或下线时递增代次丢弃在途旧结果）。
  Future<TailscalePeerRoute> peerRoute(String ip, {bool fresh = false}) async {
    final backend = _backend;
    if (backend == null || !isRunning) {
      return const TailscalePeerRoute();
    }
    if (!fresh) {
      final cached = _routeCache[ip];
      if (cached != null &&
          DateTime.now().difference(cached.$2) < _routeCacheTtl) {
        return cached.$1;
      }
    }
    for (var attempt = 0; attempt < 2; attempt++) {
      final epoch = _routeCacheEpoch;
      try {
        final route = await backend.peerRoute(ip);
        if (!isRunning) return const TailscalePeerRoute();
        if (epoch == _routeCacheEpoch) {
          _routeCache[ip] = (route, DateTime.now());
          return route;
        }
        // 期间发生过 wake/失效：丢弃旧结果，重查一次最新路径
      } catch (_) {
        return const TailscalePeerRoute();
      }
    }
    return const TailscalePeerRoute();
  }

  Future<TailscaleUdpBridge> openUdpBridge(String host, int port) {
    final backend = _backend;
    if (backend == null || !isRunning) {
      throw TailscaleException('Tailscale node is not running');
    }
    return backend.openUdpBridge(host, port);
  }

  @override
  void dispose() {
    _disposed = true;
    unawaited(_sub?.cancel());
    super.dispose();
  }
}
