part of 'posse_quic.dart';

typedef _TsStatusFn = Void Function(Pointer<Uint8>, Int32);

@Native<Void Function(Uint64, Pointer<NativeFunction<_TsStatusFn>>)>(
  symbol: 'hq_ts_set_cb',
)
external void _tsSetCb(int gen, Pointer<NativeFunction<_TsStatusFn>> cb);

@Native<Pointer<Utf8> Function(Uint64, Pointer<Utf8>, Int32)>(
  symbol: 'hq_ts_start',
)
external Pointer<Utf8> _tsStart(int gen, Pointer<Utf8> req, int len);

@Native<Pointer<Utf8> Function(Uint64)>(symbol: 'hq_ts_stop')
external Pointer<Utf8> _tsStop(int gen);

@Native<Pointer<Utf8> Function(Uint64, Pointer<Utf8>, Int32)>(
  symbol: 'hq_ts_reset',
)
external Pointer<Utf8> _tsReset(int gen, Pointer<Utf8> stateDir, int len);

@Native<Pointer<Utf8> Function(Uint64, Int32)>(symbol: 'hq_ts_login')
external Pointer<Utf8> _tsLogin(int gen, int timeoutMs);

@Native<Pointer<Utf8> Function()>(symbol: 'hq_ts_status')
external Pointer<Utf8> _tsStatus();

@Native<Void Function()>(symbol: 'hq_ts_wake')
external void _tsWake();

@Native<Pointer<Utf8> Function(Pointer<Utf8>, Int32)>(
  symbol: 'hq_ts_peer_route',
)
external Pointer<Utf8> _tsPeerRoute(Pointer<Utf8> ip, int timeoutMs);

@Native<
  Int64 Function(
    Uint64,
    Pointer<Utf8>,
    Int32,
    Pointer<NativeFunction<_EventFn>>,
  )
>(symbol: 'hq_ts_dial')
external int _tsDial(
  int gen,
  Pointer<Utf8> addr,
  int timeoutMs,
  Pointer<NativeFunction<_EventFn>> cb,
);

@Native<Pointer<Utf8> Function(Uint64, Pointer<Utf8>, Int32)>(
  symbol: 'hq_ts_udp_bridge_open',
)
external Pointer<Utf8> _tsUdpBridgeOpen(
  int gen,
  Pointer<Utf8> remoteAddr,
  int timeoutMs,
);

@Native<Void Function(Int64)>(symbol: 'hq_ts_udp_bridge_close')
external void _tsUdpBridgeClose(int id);

class TailscaleException implements Exception {
  TailscaleException(this.message);
  final String message;

  @override
  String toString() => 'TailscaleException: $message';
}

/// 内置 Tailscale 节点状态快照（与 internal/tslink.Status JSON 结构一致）。
class TailscaleStatus {
  const TailscaleStatus({
    this.enabled = false,
    this.state = 'disabled',
    this.ip = '',
    this.dnsName = '',
    this.hostname = '',
    this.controlUrl = '',
    this.authUrl = '',
    this.disableBindInterface = false,
    this.error = '',
    this.health = const [],
  });

  final bool enabled;
  final String state;
  final String ip;
  final String dnsName;
  final String hostname;
  final String controlUrl;
  final String authUrl;
  final bool disableBindInterface;
  final String error;
  final List<String> health;

  bool get isRunning => state == 'running' && ip.isNotEmpty;
  bool get needsLogin => state == 'needs_login';
  bool get needsMachineAuth => state == 'needs_machine_auth';

  factory TailscaleStatus.fromJson(Map<String, dynamic> json) {
    final rawHealth = json['health'];
    return TailscaleStatus(
      enabled: json['enabled'] == true,
      state: (json['state'] as String?) ?? 'disabled',
      ip: (json['ip'] as String?) ?? '',
      dnsName: (json['dnsName'] as String?) ?? '',
      hostname: (json['hostname'] as String?) ?? '',
      controlUrl: (json['controlUrl'] as String?) ?? '',
      authUrl: (json['authUrl'] as String?) ?? '',
      disableBindInterface: json['disableBindInterface'] == true,
      error: (json['error'] as String?) ?? '',
      health: rawHealth is List
          ? rawHealth.map((e) => e.toString()).toList(growable: false)
          : const [],
    );
  }
}

/// 到指定 Tailscale 对端的实际传输路径（与 internal/tslink.PeerRoute JSON 结构一致）。
class TailscalePeerRoute {
  const TailscalePeerRoute({
    this.kind = 'unknown',
    this.addr = '',
    this.peerRelay = '',
    this.relay = '',
  });

  final String kind;
  final String addr;
  final String peerRelay;
  final String relay;

  bool get isDirect => kind == 'direct';
  bool get isPeerRelay => kind == 'peer_relay';
  bool get isDerp => kind == 'derp';

  factory TailscalePeerRoute.fromJson(Map<String, dynamic> json) =>
      TailscalePeerRoute(
        kind: (json['kind'] as String?) ?? 'unknown',
        addr: (json['addr'] as String?) ?? '',
        peerRelay: (json['peerRelay'] as String?) ?? '',
        relay: (json['relay'] as String?) ?? '',
      );
}

class TailscaleResetResult {
  const TailscaleResetResult({
    required this.status,
    required this.loggedOut,
    required this.logoutError,
    required this.localCleared,
  });

  final TailscaleStatus status;
  final bool loggedOut;
  final String logoutError;
  final bool localCleared;

  factory TailscaleResetResult.fromJson(Map<String, dynamic> json) =>
      TailscaleResetResult(
        status: TailscaleStatus.fromJson(
          (json['status'] as Map?)?.cast<String, dynamic>() ?? const {},
        ),
        loggedOut: json['loggedOut'] == true,
        logoutError: (json['logoutError'] as String?) ?? '',
        localCleared: json['localCleared'] == true,
      );
}

/// 供原生 DeskSession 使用的本地回环 UDP 桥（127.0.0.1:[localPort] <-> tsnet UDP）。
class TailscaleUdpBridge {
  TailscaleUdpBridge._(this.id, this.localPort);

  final int id;
  final int localPort;
  var _closed = false;

  bool get isClosed => _closed;

  void close() {
    if (_closed) return;
    _closed = true;
    _tsUdpBridgeClose(id);
  }
}

final _tsUpdates = StreamController<TailscaleStatus>.broadcast();

void _onTsStatus(Pointer<Uint8> data, int len) {
  if (data == nullptr) return;
  final bytes = Uint8List.fromList(data.asTypedList(len));
  _free(data);
  try {
    final map = jsonDecode(utf8.decode(bytes)) as Map<String, dynamic>;
    _tsUpdates.add(TailscaleStatus.fromJson(map));
  } catch (_) {}
}

/// 延迟注册一次状态回调；首次调用前先经 _engineGen 完成引擎初始化并绑定代次。
final _tsCallback = () {
  final gen = _engineGen;
  final cb = NativeCallable<_TsStatusFn>.listener(_onTsStatus);
  _tsSetCb(gen, cb.nativeFunction);
  return cb;
}();

String _takeCString(Pointer<Utf8> ptr) {
  if (ptr == nullptr) return '{}';
  try {
    return ptr.toDartString();
  } finally {
    _free(ptr.cast());
  }
}

/// 进程级内置 Tailscale（tsnet）节点控制面，供手机端状态管理、登录、路由查询与 UDP 桥接使用。
abstract final class TailscaleNode {
  /// 节点状态变化流（注册回调时会立刻推送一次当前快照）。
  static Stream<TailscaleStatus> get updates {
    _tsCallback;
    return _tsUpdates.stream;
  }

  /// 读取当前节点状态快照（非阻塞）。
  static TailscaleStatus status() {
    final text = _takeCString(_tsStatus());
    final map = jsonDecode(text) as Map<String, dynamic>;
    return TailscaleStatus.fromJson(map);
  }

  /// 在后台 isolate 启动或重配置内置 Tailscale 节点。
  static Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) {
    _tsCallback;
    final gen = _engineGen;
    final payload = jsonEncode({
      'stateDir': stateDir,
      if (hostname.isNotEmpty) 'hostname': hostname,
      if (controlUrl.isNotEmpty) 'controlUrl': controlUrl,
      if (authKey.isNotEmpty) 'authKey': authKey,
      'disableBindInterface': disableBindInterface,
    });
    return Isolate.run(() {
      final req = payload.toNativeUtf8();
      try {
        final raw = _takeCString(_tsStart(gen, req, req.length));
        final map = jsonDecode(raw) as Map<String, dynamic>;
        final err = (map['err'] as String?) ?? '';
        if (err.isNotEmpty) throw TailscaleException(err);
        return TailscaleStatus.fromJson(
          (map['status'] as Map?)?.cast<String, dynamic>() ?? const {},
        );
      } finally {
        malloc.free(req);
      }
    });
  }

  /// 在后台 isolate 停止内置 Tailscale 节点并关闭全部 UDP 桥。
  static Future<TailscaleStatus> stop() {
    _tsCallback;
    final gen = _engineGen;
    return Isolate.run(() {
      final raw = _takeCString(_tsStop(gen));
      final map = jsonDecode(raw) as Map<String, dynamic>;
      final err = (map['err'] as String?) ?? '';
      if (err.isNotEmpty) throw TailscaleException(err);
      return TailscaleStatus.fromJson(
        (map['status'] as Map?)?.cast<String, dynamic>() ?? const {},
      );
    });
  }

  /// 在后台 isolate 注销并清除本地内置 Tailscale 身份状态。
  static Future<TailscaleResetResult> reset({String stateDir = ''}) {
    _tsCallback;
    final gen = _engineGen;
    return Isolate.run(() {
      final dir = stateDir.toNativeUtf8();
      try {
        final raw = _takeCString(_tsReset(gen, dir, dir.length));
        final map = jsonDecode(raw) as Map<String, dynamic>;
        final err = (map['err'] as String?) ?? '';
        if (err.isNotEmpty) throw TailscaleException(err);
        return TailscaleResetResult.fromJson(map);
      } finally {
        malloc.free(dir);
      }
    });
  }

  /// 在后台 isolate 触发交互式浏览器登录以获取/刷新 AuthURL。
  static Future<TailscaleStatus> startLogin({
    Duration timeout = const Duration(seconds: 5),
  }) {
    _tsCallback;
    final gen = _engineGen;
    final ms = timeout.inMilliseconds;
    return Isolate.run(() {
      final raw = _takeCString(_tsLogin(gen, ms));
      final map = jsonDecode(raw) as Map<String, dynamic>;
      final err = (map['err'] as String?) ?? '';
      if (err.isNotEmpty) throw TailscaleException(err);
      return TailscaleStatus.fromJson(
        (map['status'] as Map?)?.cast<String, dynamic>() ?? const {},
      );
    });
  }

  /// 通知 tsnet 底层网络监控（用于前后台切换或 Wi-Fi/蜂窝漫游加速重探）。
  static void wake() => _tsWake();

  /// 查询到对端 Tailscale IP 的实际路径（直连 / PeerRelay / DERP）。
  static Future<TailscalePeerRoute> peerRoute(
    String ip, {
    Duration timeout = const Duration(milliseconds: 1500),
  }) {
    final ms = timeout.inMilliseconds;
    return Isolate.run(() {
      final p = ip.toNativeUtf8();
      try {
        final raw = _takeCString(_tsPeerRoute(p, ms));
        final map = jsonDecode(raw) as Map<String, dynamic>;
        return TailscalePeerRoute.fromJson(map);
      } finally {
        malloc.free(p);
      }
    });
  }

  /// 为看桌面原生 DeskSession 打开一条回环 UDP 桥（127.0.0.1:localPort <-> tsnet UDP）。
  static Future<TailscaleUdpBridge> openUdpBridge(
    String host,
    int port, {
    Duration timeout = const Duration(seconds: 5),
  }) {
    _tsCallback;
    final gen = _engineGen;
    final remote = host.contains(':') ? '[$host]:$port' : '$host:$port';
    final ms = timeout.inMilliseconds;
    return Isolate.run(() {
      final p = remote.toNativeUtf8();
      try {
        final raw = _takeCString(_tsUdpBridgeOpen(gen, p, ms));
        final map = jsonDecode(raw) as Map<String, dynamic>;
        final err = (map['err'] as String?) ?? '';
        if (err.isNotEmpty) throw TailscaleException(err);
        final id = (map['id'] as num?)?.toInt() ?? 0;
        final localPort = (map['localPort'] as num?)?.toInt() ?? 0;
        if (id <= 0 || localPort <= 0) {
          throw TailscaleException('invalid udp bridge response');
        }
        return TailscaleUdpBridge._(id, localPort);
      } finally {
        malloc.free(p);
      }
    });
  }
}
