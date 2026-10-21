import 'dart:async';

import 'rpc.dart';

/// 和电脑之间的往返延迟：每 [interval] 发一次 `runtime.ping`（电脑上的 hive 太旧没有这个方法时改用
/// `status.get`），取最近 3 次的中位数，一次抖动不会让数字乱跳。
///
/// 同一条连接上的回复是一条条发的，图片、视频分段、终端快照这类大块数据在路上时，ping 要排在它们后面，
/// 测出来的不是网络延迟：这期间收发超过 [_crowded] 的样本不算。
///
/// 顺带发现死掉的连接：切网络、手机休眠后 TCP 可能半开着，系统迟迟不报错。连续 [misses] 次超时、
/// 期间没有收到任何请求的回复，就 [PhoneRpc.drop] 交给重连；超时后不等间隔接着测，从停止回应算起
/// 最多 [interval] + [misses]×[timeout]（默认 20 秒）。期间有别的回复回来（慢线路上分段下大文件，
/// ping 排在后面）不算没响应。终端输出、标签这类推送不算：电脑那边这条连接处理请求卡住时推送照样在发，
/// 按收到的字节算的话连接永远"活着"，所有请求一直超时，只能重开 App。
class LatencyMeter {
  LatencyMeter(
    this.rpc, {
    required this.onChange,
    this.interval = const Duration(seconds: 5),
    this.timeout = const Duration(seconds: 5),
    this.misses = 3,
  });

  final PhoneRpc rpc;

  /// 显示的读数（[latency]、[unresponsive]）变了时调用。
  final void Function() onChange;
  final Duration interval;
  final Duration timeout;
  final int misses;

  static const _crowded = 64 << 10;

  final _samples = <Duration>[];
  var _missed = 0;
  var _method = 'runtime.ping';
  var _inFlight = false;
  var _stopped = false;
  Timer? _timer;

  /// 最近几次的中位数；还没测到为空。
  Duration? get latency {
    if (_samples.isEmpty) return null;
    final sorted = [..._samples]..sort();
    return sorted[sorted.length ~/ 2];
  }

  /// 最近一次 ping 超时、期间也没收到任何数据。
  bool get unresponsive => _missed > 0;

  void start() {
    unawaited(tick());
    _timer = Timer.periodic(interval, (_) => unawaited(tick()));
  }

  void stop() {
    _stopped = true;
    _timer?.cancel();
  }

  /// 测一次。上一次还没回来就跳过，不在慢线路上越堆越多。
  Future<void> tick() async {
    if (_inFlight || _stopped) return;
    _inFlight = true;
    final before = (latency, unresponsive);
    final rx = rpc.receivedBytes, tx = rpc.sentBytes;
    final replies = rpc.repliesReceived;
    final watch = Stopwatch()..start();
    try {
      await rpc.call(_method, const {}, timeout: timeout);
      watch.stop();
      _missed = 0;
      final moved = rpc.receivedBytes - rx + rpc.sentBytes - tx;
      if (moved <= _crowded) {
        _samples.add(watch.elapsed);
        if (_samples.length > 3) _samples.removeAt(0);
      }
    } on RpcException catch (e) {
      if (e.code == 'method_not_found' || e.code == 'unknown_method') {
        _method = 'status.get';
      }
      // closed：连接已经断了，重连那边会处理
    } on TimeoutException {
      // 这段时间还有别的回复回来：电脑在处理请求，只是 ping 排在大回复后面
      if (rpc.repliesReceived == replies) _missed++;
    } catch (_) {
      // 连接关掉后再发会抛错，下一轮这个测量器也停了
    } finally {
      _inFlight = false;
    }
    if (_stopped) return;
    if (_missed >= misses) {
      stop();
      rpc.drop();
    }
    if ((latency, unresponsive) != before) onChange();
    // 超时那一刻定时器多半也刚到，当时这一次还算在路上被跳过，要再等一整个间隔：
    // 没响应时马上补测，3 次超时大约 3×[timeout] 就能断开重连
    if (_missed > 0 && !_stopped) unawaited(tick());
  }
}
