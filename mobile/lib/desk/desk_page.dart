import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/scheduler.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../protocol/pairing.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import 'chrome.dart';
import 'display_picker.dart';
import 'displays.dart';
import 'direct.dart';
import 'events.dart';
import 'gestures.dart';
import 'hint.dart';
import 'key_bar.dart';
import 'keys.dart';
import 'mods.dart';
import 'plugin.dart';
import 'status.dart';
import 'toolbar.dart';
import 'unlock.dart';
import 'viewport.dart';
import 'wire.dart';

/// 留空和画面要能分开：画面是黑的，留空用很深的暖灰。
const deskLetterbox = Color(0xFF2A2926);
const _picture = Color(0xFF000000);
const _onPicture = Color(0xFFC8C6C1);

const _markSize = 22.0;
const _markFade = Duration(milliseconds: 280);
const _cursorRingSize = 16.0;

/// 左缘拖动是画面平移，Listener 不参与手势竞争，和页面返回会一起发生。
/// 返回放在工具条上。
class DeskRoute extends MaterialPageRoute<void> {
  DeskRoute({required super.builder});

  @override
  bool get popGestureEnabled => false;
}

/// 看 Mac 桌面。进页面订阅，UDP 用这条连接派生的密钥。
class DeskPage extends StatefulWidget {
  const DeskPage({super.key, required this.model, this.receiver});

  final PosseAppModel model;
  final DeskReceiver? receiver;

  @override
  State<DeskPage> createState() => _DeskPageState();
}

class _Mark {
  _Mark(this.pointer, this.at);

  final int pointer;
  Offset at;
  Duration? fadeAt;
  var solid = false;
}

const _padded = TextEditingValue(
  text: deskTypingPad,
  selection: TextSelection.collapsed(offset: deskTypingPad.length),
);

class _DeskPageState extends State<DeskPage>
    with WidgetsBindingObserver, SingleTickerProviderStateMixin {
  late final DeskReceiver _receiver = widget.receiver ?? DeskReceiver();
  late final DeskGestures _gestures;
  final _log = DeskEventLog();
  final _mods = DeskMods();
  final _watch = Stopwatch();
  final _marks = <_Mark>[];
  final _keyboard = FocusNode();
  final _input = TextEditingController.fromValue(_padded);
  // 隐藏输入框上一次的内容，和这一次比出打了什么、删了几个
  String _shadow = deskTypingPad;
  final _chrome = DeskChrome();
  final _edges = DeskEdges();
  final _lease = Object();
  // 竖屏默认右上角，横屏默认没有刘海那一侧的中间（第一次横屏时按刘海定左右）
  final _anchorTop = ValueNotifier<Offset>(const Offset(1, 0));
  final _anchorSide = ValueNotifier<Offset>(const Offset(1, 0.5));
  var _sidePlaced = false;

  /// 最后一次单击在舞台里的纵坐标。键盘弹出时画面上移，让它不被盖住。
  double? _typeY;

  StreamSubscription<DeskStats>? _statsSub;
  StreamSubscription<Map<String, Object?>>? _deskFrames;
  RpcStream? _deskSub;
  PhoneRpc? _rpcNow;
  Timer? _barTimer;
  Timer? _retry;
  Timer? _lockRetry;
  late final DeskInputLink _link;
  final _scrollCarry = DeskScrollCarry();
  final _packets = <Uint8List>[];
  var _sendTail = Future<void>.value();
  final _inputLine = ValueNotifier<(int, int)>((0, 0));
  Set<DeskMod> _dragMods = {};
  var _moveScheduled = false;
  late final Ticker _ticker;
  final _frame = ValueNotifier<int>(0);
  final _statsTick = ValueNotifier<DeskStats>(const DeskStats());
  DeskStats _stats = const DeskStats();

  /// 这次接收已经请 Mac 打过洞了：每次开接收只打一次，打不通就一直走中转。
  bool _punchAsked = false;

  /// Mac 上的屏和正在看的那块，订阅流里的 displays 事件给。旧电脑不给，一直是空的。
  DeskDisplays _displays = DeskDisplays.none;
  int? _texture;
  String? _fail;
  String? _endNote;
  String? _accessNote;
  // Mac 锁着屏：画面照常，上方给一个输密码解锁的入口。旧电脑锁屏时结束观看，不报这个
  var _macLocked = false;
  // 这台电脑在手机钥匙串里记着登录密码：锁屏时一点就解
  var _passwordSaved = false;
  final _passwords = const DeskPasswordStore();
  // 勾了记住、刚发出去的密码：Mac 随后解开了才存，输错的不记
  ({String key, String password, DateTime at})? _pendingSave;
  // 切画面的结果（窗口没了回到整块屏），几秒后消失
  String? _viewNote;
  Timer? _noteTimer;
  var _indirect = false;
  String? _customHost;
  int? _customPort;
  int? _readyPort;

  /// 当前连接走隧道、画面改走同一台电脑的局域网地址时是那个地址。
  String? _lanHost;
  Timer? _lanCheck;

  /// 当前连接走隧道，同一台电脑也没存局域网地址。Mac 配了中转还能看。
  var _noDirect = false;

  /// Mac 给的 UDP 中转（host:port）和这次观看的令牌。
  String? _relay;
  Uint8List? _relayToken;
  // 电脑认缺包点名补发（ready 里带 nack）
  bool _nack = false;
  DeskTrafficKeys? _keys;
  String? _hostNow;
  var _watchTail = Future<void>.value();
  var _up = false;
  var _background = false;
  var _debugOpen = false;

  /// 悬浮球里点「手势」重新打开的提示，和第一次进页面的是同一层。
  var _hintOpen = false;
  var _keyboardUp = false;
  var _stopped = false;
  var _gen = 0;
  SystemUiMode? _ui;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    widget.model.addListener(_followComputer);
    _gestures = DeskGestures(
      width: 1,
      height: 1,
      aspect: 16 / 9,
      onEvent: _onGesture,
      onArm: _holdFeedback,
    );
    _watch.start();
    // 跟指针事件共用这块表。Ticker 自己的时间在停掉再开时会从零开始。
    _ticker = createTicker((_) {
      if (!mounted) return;
      _gestures.advance(_now);
      _marks.removeWhere((mark) {
        final at = mark.fadeAt;
        return at != null && _now - at > _markFade;
      });
      _frame.value++;
      _syncTicker();
    });
    _link = DeskInputLink(
      onPacket: (packet) {
        _packets.add(packet);
        _kickSend();
        _syncInputLine();
      },
    );
    _keyboard.addListener(_keyboardChanged);
    _input.addListener(_onTyped);
    if (deskSupported) {
      _statsSub = _receiver.stats().listen(_onStats);
    }
    deskChromeLease.acquire(_lease);
    unawaited(lockDeskOrientation(widget.model.settings.deskLandscape));
    unawaited(_edges.set(true));
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) unawaited(_ensure());
    });
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    widget.model.removeListener(_followComputer);
    _noteTimer?.cancel();
    _ticker.dispose();
    _frame.dispose();
    _statsTick.dispose();
    _anchorTop.dispose();
    _anchorSide.dispose();
    _retry?.cancel();
    _lockRetry?.cancel();
    _lanCheck?.cancel();
    // 让还在跑的订阅停在下一次检查，松开先于退订发出。
    _gen++;
    _link.release();
    _retry?.cancel();
    final tail = _sendTail;
    _inputLine.dispose();
    unawaited(() async {
      await tail;
      await _drainPackets();
      await _dropDesk(unsubscribe: true);
      await _stopReceiver();
    }());
    unawaited(_statsSub?.cancel());
    _keyboard.removeListener(_keyboardChanged);
    _input.removeListener(_onTyped);
    _keyboard.dispose();
    _input.dispose();
    _barTimer?.cancel();
    if (_ui != null) {
      SystemChrome.setEnabledSystemUIMode(SystemUiMode.edgeToEdge);
    }
    if (deskChromeLease.release(_lease)) {
      unawaited(restoreDeskOrientation());
      unawaited(_edges.set(false));
    }
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    switch (state) {
      case AppLifecycleState.paused:
      case AppLifecycleState.hidden:
        _setBackground(true);
        break;
      case AppLifecycleState.resumed:
        _setBackground(false);
        break;
      case AppLifecycleState.inactive:
      case AppLifecycleState.detached:
        break;
    }
  }

  String? get _autoHost => deskHostOf(widget.model.connectedUrl);

  /// 直连发到哪。经隧道连着又没有同一台电脑的局域网地址时为空，只能靠中转。
  String? get _udpHost {
    final custom = _customHost;
    if (custom != null && custom.isNotEmpty) return custom;
    if (_noDirect) return null;
    return _lanHost ?? _autoHost;
  }

  /// 同一台电脑（主机公钥相同）存着的所有地址的主机名，当前连接的排前面。
  List<String> _sameComputerHosts() {
    final offer = widget.model.offer;
    if (offer == null) return const [];
    final urls = [...offer.dialUrls];
    for (final c in widget.model.computers) {
      try {
        final other = PairingOffer.parse(c.code);
        if (other.publicKeyB64 == offer.publicKeyB64) {
          urls.addAll(other.dialUrls);
        }
      } on Object {
        // 存坏的配对码跳过。
      }
    }
    return [for (final url in urls) ?deskHostOf(url)];
  }

  bool get _custom => _customHost != null || _customPort != null;

  bool get _immersive => _stats.video && !_background;

  void _onGesture(DeskEvent event) {
    if (deskHapticFor(event)) unawaited(HapticFeedback.lightImpact());
    _log.add(event);
    switch (event) {
      case DeskTapEvent(:final at, :final clicks):
        _click(at, deskButtonLeft, clicks);
        _typeY = _gestures.lastTap?.dy;
        if (_keyboardUp) setState(() {});
      case DeskRightClickEvent(:final at):
        _click(at, deskButtonRight, 1);
      case DeskDragEvent(:final phase, :final at):
        _drag(phase, at);
      case DeskPointEvent(:final at):
        // 不单独成包：紧接着的滚动和它进同一个包，Mac 先移指针再滚
        _link.move(deskAxis(at.x), deskAxis(at.y));
        _scheduleMove();
      case DeskScrollEvent(:final dx, :final dy):
        final (ux, uy) = deskScrollUnits(
          dx,
          dy,
          _gestures.fit.height * _gestures.viewport.zoom,
        );
        final (sx, sy) = _scrollCarry.take(ux, uy);
        if (sx != 0 || sy != 0) _link.scroll(sx, sy);
      case DeskKeyEvent():
      case DeskTextEvent():
        break;
    }
    _noteInput();
  }

  Set<DeskMod> _lockedMods() => {
    for (final mod in DeskMod.values)
      if (_mods.mode(mod) == DeskModMode.locked) mod,
  };

  void _click(DeskPoint at, int button, int clicks) {
    final x = deskAxis(at.x);
    final y = deskAxis(at.y);
    final locked = _lockedMods();
    final bits = deskModBits(locked);
    _link.batch(() {
      _modsDown(locked, bits);
      _link.buttonDown(button, clicks, x, y);
      _link.buttonUp(button, clicks, x, y);
      _modsUp(locked, bits);
    });
  }

  void _drag(DeskDragPhase phase, DeskPoint at) {
    final x = deskAxis(at.x);
    final y = deskAxis(at.y);
    switch (phase) {
      case DeskDragPhase.down:
        _dragMods = _lockedMods();
        final bits = deskModBits(_dragMods);
        _link.batch(() {
          _modsDown(_dragMods, bits);
          _link.buttonDown(deskButtonLeft, 1, x, y);
        });
      case DeskDragPhase.move:
        _link.move(x, y);
        _scheduleMove();
      case DeskDragPhase.up:
        final mods = _dragMods;
        _dragMods = {};
        _link.batch(() {
          _link.buttonUp(deskButtonLeft, 1, x, y);
          _modsUp(mods, deskModBits(mods));
        });
    }
  }

  void _modsDown(Set<DeskMod> mods, int bits) {
    for (final mod in deskModPressOrder) {
      if (!mods.contains(mod)) continue;
      _link.keyDown(deskModHid(mod), bits);
    }
  }

  void _modsUp(Set<DeskMod> mods, int bits) {
    for (final mod in deskModPressOrder.reversed) {
      if (!mods.contains(mod)) continue;
      _link.keyUp(deskModHid(mod), bits);
    }
  }

  void _scheduleMove() {
    if (_moveScheduled) return;
    _moveScheduled = true;
    SchedulerBinding.instance.scheduleFrameCallback((_) {
      _moveScheduled = false;
      if (!mounted) return;
      _link.flushMove();
    });
  }

  void _syncInputLine() {
    _inputLine.value = (_link.sent, _link.confirmed);
  }

  void _noteInput() {
    _syncInputLine();
    if (_debugOpen && mounted) setState(() {});
  }

  void _kickSend() {
    _sendTail = _sendTail.then((_) => _drainPackets());
    // 测试里没有确认号，周期重发会让 pumpAndSettle 停不下来。
    if (_link.needsRetry && deskSupported) _armRetry();
  }

  void _armRetry() {
    if (_retry != null) return;
    _retry = Timer(const Duration(milliseconds: 100), () {
      _retry = null;
      if (!mounted) return;
      if (!_link.needsRetry && !_link.hasFreshMove) return;
      _link.retransmit();
    });
  }

  Future<void> _drainPackets() async {
    while (_packets.isNotEmpty) {
      final packet = _packets.removeAt(0);
      if (!deskSupported) continue;
      try {
        await _receiver.sendInput(packet);
      } on PlatformException {
        // 接收还没起来。必达事件留在未确认列表里，下一次重发。
      }
    }
  }

  void _holdFeedback() {
    unawaited(HapticFeedback.lightImpact());
  }

  void _toggleBar() {
    setState(() => _chrome.toggleExpanded(_now));
    _armBarTimer();
  }

  void _pokeBar() {
    _chrome.noteActivity(_now);
    _armBarTimer();
  }

  void _armBarTimer() {
    _barTimer?.cancel();
    final from = _chrome.idleFrom;
    if (!_chrome.expanded || from == null) return;
    _barTimer = Timer(deskBarIdle, () {
      if (!mounted || from != _chrome.idleFrom) return;
      if (_chrome.collapseIfIdle(from + deskBarIdle)) setState(() {});
    });
  }

  /// 横屏第一次出现时按刘海定左右，之后由用户拖。
  ValueNotifier<Offset> _sideAnchor(EdgeInsets pad) {
    if (!_sidePlaced) {
      _sidePlaced = true;
      final right = deskBarOnRight(left: pad.left, right: pad.right);
      _anchorSide.value = Offset(right ? 1 : 0, _anchorSide.value.dy);
    }
    return _anchorSide;
  }

  void _dragBar() {
    _chrome.noteActivity(_now);
    _armBarTimer();
  }

  void _toggleOrientation() {
    final landscape =
        MediaQuery.orientationOf(context) == Orientation.landscape;
    final next = !landscape;
    widget.model.updateSettings((s) => s.deskLandscape = next);
    unawaited(lockDeskOrientation(next));
    _pokeBar();
    setState(() {});
  }

  void _toggleShortcuts() {
    setState(() => _chrome.toggleShortcuts());
    _pokeBar();
  }

  void _resetZoom() {
    _pointer(() {
      _gestures.viewport.zoom = deskMinZoom;
      _gestures.viewport.panX = 0;
      _gestures.viewport.panY = 0;
    });
    _pokeBar();
  }

  void _toggleDebug() {
    setState(() => _debugOpen = !_debugOpen);
    _pokeBar();
  }

  void _showHint() {
    setState(() => _hintOpen = true);
    _pokeBar();
  }

  void _dismissHint() {
    if (!widget.model.settings.deskGesturesSeen) {
      widget.model.updateSettings((s) => s.deskGesturesSeen = true);
    }
    setState(() => _hintOpen = false);
  }

  Future<void> _disconnect() {
    _pokeBar();
    if (_stopped) return _enqueue(() => _ensureBody(force: true));
    _stopped = true;
    _lockRetry?.cancel();
    _lockRetry = null;
    return _enqueue(_disconnectBody);
  }

  Future<void> _disconnectBody() async {
    _gen++;
    _stats = const DeskStats();
    _statsTick.value = const DeskStats();
    if (mounted) {
      setState(() {
        _fail = null;
        _endNote = null;
        _accessNote = null;
        _macLocked = false;
        _indirect = false;
        _up = false;
        _texture = null;
      });
    }
    await _releaseHeld();
    await _dropDesk(unsubscribe: true);
    await _stopReceiver();
    _keys = null;
    _readyPort = null;
  }

  void _followComputer() {
    if (!mounted || _stopped || _background) return;
    final rpc = widget.model.rpc;
    final host = _autoHost;
    if (identical(rpc, _rpcNow) && host == _hostNow) return;
    if (rpc == null && _rpcNow == null) return;
    unawaited(_enqueue(() => _ensureBody(force: false)));
  }

  void _keyboardChanged() {
    final up = _keyboard.hasFocus;
    if (!mounted || up == _keyboardUp) return;
    setState(() => _keyboardUp = up);
  }

  void _setBackground(bool background) {
    if (_background == background) return;
    _background = background;
    if (background) {
      _lockRetry?.cancel();
      _lockRetry = null;
      unawaited(_enqueue(_pauseBody));
    } else if (!_stopped) {
      unawaited(_enqueue(() => _ensureBody(force: true)));
    }
    if (mounted) setState(() {});
  }

  Future<void> _pauseBody() async {
    _gen++;
    await _releaseHeld();
    await _dropDesk(unsubscribe: true);
    await _stopReceiver();
    _keys = null;
    if (!mounted) return;
    setState(() {
      _up = false;
      _texture = null;
    });
  }

  /// 帧率和码率只刷新胶囊。出画面、画面尺寸变了才重建整页（顶栏和比例）。
  void _onStats(DeskStats stats) {
    if (!mounted) return;
    if (_link.noteAck(stats.ack)) _syncInputLine();
    final relayout =
        stats.video != _stats.video ||
        stats.width != _stats.width ||
        stats.height != _stats.height ||
        stats.view != _stats.view;
    _stats = stats;
    _statsTick.value = stats;
    if (relayout) setState(() {});
    final public = stats.public;
    if (public != null && _relay != null && !_punchAsked) {
      _punchAsked = true;
      unawaited(_punch(public));
    }
  }

  /// 画面经中转到了之后打洞：把手机接收 socket 的公网地址经 desk.punch 交给 Mac，换回 Mac 出口的公网地址，
  /// 两边互发打洞包。打通了 Mac 改发直连，打不通照旧走中转。
  Future<void> _punch(String phone) async {
    final rpc = _rpcNow;
    final gen = _gen;
    if (rpc == null) return;
    try {
      final result = await rpc.call('desk.punch', {'addr': phone});
      final mac = result['addr'];
      if (!mounted || gen != _gen || mac is! String) return;
      await _receiver.punch(mac);
    } on Object {
      // 旧版电脑没有 desk.punch、中转没回探测，都照旧走中转。
    }
  }

  /// 订阅、退订和改 UDP 地址串行，避免停接收和开接收叠在一起。
  Future<void> _enqueue(Future<void> Function() body) {
    final run = _watchTail.then((_) async {
      if (!mounted) return;
      await body();
    });
    _watchTail = run.then((_) {}, onError: (_) {});
    return run;
  }

  Future<void> _ensure({bool force = false}) {
    return _enqueue(() => _ensureBody(force: force));
  }

  Future<void> _ensureBody({required bool force}) async {
    if (_stopped && !force) return;
    if (force) {
      _stopped = false;
      _lockRetry?.cancel();
      _lockRetry = null;
    }
    // 没有桌面插件（测试）时先返回，不会去解析域名。
    if (!deskSupported) {
      setState(() => _fail = '这台设备不能看桌面');
      return;
    }
    final rpc = widget.model.rpc;
    final host = _autoHost;
    if (rpc == null || host == null || host.isEmpty) {
      _gen++;
      await _releaseHeld();
      await _dropDesk(unsubscribe: false);
      await _stopReceiver();
      _keys = null;
      _readyPort = null;
      _rpcNow = null;
      _hostNow = null;
      if (!mounted) return;
      setState(() {
        _fail = null;
        _endNote = null;
        _accessNote = null;
        _macLocked = false;
        _indirect = false;
        _up = false;
        _texture = null;
      });
      return;
    }
    if (!force &&
        identical(rpc, _rpcNow) &&
        host == _hostNow &&
        _deskSub != null &&
        _fail == null &&
        _endNote == null &&
        !_indirect) {
      return;
    }
    final gen = ++_gen;
    _rpcNow = rpc;
    _hostNow = host;
    _stats = const DeskStats();
    _statsTick.value = const DeskStats();
    setState(() {
      // 可能换了一台电脑，等这次订阅报来的屏
      _displays = DeskDisplays.none;
      _fail = null;
      _endNote = null;
      _accessNote = null;
      _macLocked = false;
      _indirect = false;
      _up = false;
      _texture = null;
    });
    await _releaseHeld();
    if (!mounted || gen != _gen) return;
    await _dropDesk(unsubscribe: false);
    await _stopReceiver();
    _keys = null;
    _readyPort = null;
    if (!mounted || gen != _gen) return;
    final udp = await deskUdpHost(host, _sameComputerHosts());
    if (!mounted || gen != _gen) return;
    // 没有直连地址也先订阅：Mac 配了中转时 ready 里会带上，没有再提示
    _noDirect = udp == null;
    _lanHost = udp == null || udp == host ? null : udp;
    final pixels = _screenPixels();
    final RpcStream stream;
    try {
      stream = await rpc.subscribe('desk.subscribe', {
        'width': pixels.$1,
        'height': pixels.$2,
        // 插件只会解 HEVC。
        'codecs': ['hevc'],
        'display': ?widget.model.settings.deskDisplays[_computerKey],
      });
    } on Object {
      if (!mounted || gen != _gen) return;
      setState(() => _fail = deskFailText('rpc'));
      return;
    }
    if (!mounted || gen != _gen) {
      await stream.cancel();
      return;
    }
    _deskSub = stream;
    _deskFrames = stream.frames.listen(
      (frame) {
        unawaited(_enqueue(() => _onDeskFrame(frame, gen, rpc)));
      },
      onError: (Object error) {
        unawaited(_enqueue(() => _onDeskError(error, gen)));
      },
    );
  }

  Future<void> _onDeskFrame(
    Map<String, Object?> frame,
    int gen,
    PhoneRpc rpc,
  ) async {
    if (!mounted || gen != _gen) return;
    switch (frame['type']) {
      case 'ready':
        await _onReady(frame, gen, rpc);
      case 'state':
        if (!mounted) return;
        switch (frame['state']) {
          case 'no_accessibility':
            setState(() => _accessNote = deskAccessText);
          case 'accessibility':
            setState(() => _accessNote = null);
          case 'locked':
            setState(() => _macLocked = true);
            unawaited(_checkSaved());
          case 'unlocked':
            setState(() => _macLocked = false);
            unawaited(_savePending());
        }
      case 'displays':
        if (!mounted) return;
        final displays = DeskDisplays.parse(frame);
        // 换了在看的屏或窗口（手动切、窗口没了退回整块屏、锁屏改看主屏），
        // 原来的指针位置在新画面里没有意义
        if (displays.current != _displays.current ||
            displays.window?.id != _displays.window?.id) {
          _gestures.cursor = const DeskPoint(0.5, 0.5);
        }
        setState(() => _displays = displays);
        if (displays.gone) _note('窗口关掉了或不在这个空间，回到整块屏');
      case 'end':
        final reason = frame['reason'];
        await _finishWatch(gen, reason is String ? reason : '');
    }
  }

  /// 这台电脑的公钥：按它记上次看的屏，换地址、重新扫码都不变。
  String? get _computerKey => widget.model.offer?.publicKeyB64;

  /// 悬浮球的「画面」：选一块屏或一个窗口。选的屏记下来，窗口不记（关掉就没了）。让 Mac 切过去，
  /// 切好了 Mac 推新的 displays，画面也换过来；新画面的大小、比例和原来的不一样，缩放回到 1 倍。
  Future<void> _pickView() async {
    _pokeBar();
    final rpc = _rpcNow;
    if (rpc == null) return;
    final pick = await pickDeskView(
      context,
      _displays,
      windows: rpc.call('desk.windows', const {}).then(DeskWindow.parseList),
    );
    if (!mounted || pick == null || !identical(rpc, _rpcNow)) return;
    if (pick.window) {
      if (pick.id == _displays.window?.id) return;
    } else {
      if (pick.id == _displays.current && _displays.window == null) return;
      final key = _computerKey;
      if (key != null) {
        widget.model.updateSettings((s) => s.deskDisplays[key] = pick.id);
      }
    }
    _resetZoom();
    try {
      await rpc.call(pick.window ? 'desk.window' : 'desk.display', {
        'id': pick.id,
      });
    } on Object {
      // 没在看了、连接断了：下次订阅照记下的屏开始
    }
  }

  /// 画面上方一句几秒后自己消失的提示。
  void _note(String text) {
    _noteTimer?.cancel();
    setState(() => _viewNote = text);
    _noteTimer = Timer(const Duration(seconds: 4), () {
      if (mounted) setState(() => _viewNote = null);
    });
  }

  Future<void> _onReady(
    Map<String, Object?> frame,
    int gen,
    PhoneRpc rpc,
  ) async {
    final port = _jsonInt(frame['port']);
    final codec = frame['codec'];
    final nonceB64 = frame['nonceB64'];
    if (port == null || port < 1 || port > 65535 || nonceB64 is! String) {
      await _applyFail('capture_failed', gen);
      return;
    }
    if (codec != 'hevc') {
      await _applyFail('unsupported_codec', gen);
      return;
    }
    final Uint8List nonce;
    try {
      nonce = base64.decode(nonceB64);
    } on Object {
      await _applyFail('capture_failed', gen);
      return;
    }
    final DeskTrafficKeys keys;
    try {
      keys = deriveDeskKeys(
        recvKey: rpc.session.recvKey,
        sendKey: rpc.session.sendKey,
        nonce: nonce,
      );
    } on Object {
      await _applyFail('capture_failed', gen);
      return;
    }
    final host = _udpHost;
    final udpPort = _customPort ?? port;
    final relay = frame['relay'];
    final relayTokenB64 = frame['relayTokenB64'];
    _relay = relay is String && relay.isNotEmpty ? relay : null;
    _nack = frame['nack'] == true;
    _relayToken = null;
    if (_relay != null && relayTokenB64 is String) {
      try {
        _relayToken = base64.decode(relayTokenB64);
      } on Object {
        _relay = null;
      }
    }
    if ((host == null || host.isEmpty) && _relay == null) {
      await _dropDesk(unsubscribe: true);
      if (mounted && gen == _gen) setState(() => _indirect = true);
      return;
    }
    _keys = keys;
    _readyPort = port;
    try {
      await _stopReceiver();
      if (!mounted || gen != _gen) return;
      final id = await _receiver.start(
        host: host,
        port: host == null ? null : udpPort,
        relay: _relay,
        relayToken: _relayToken,
        keyToPhone: keys.toPhone,
        keyToMac: keys.toMac,
        codec: DeskCodec.hevc,
        nack: _nack,
      );
      if (!mounted || gen != _gen) {
        await _stopReceiver();
        return;
      }
      _link.reset();
      _syncInputLine();
      setState(() {
        _texture = id;
        _up = true;
        _fail = null;
        _endNote = null;
      });
      // 局域网地址是猜的、中转也可能不通：hello 到不了时 Mac 要等 60 秒才放弃，这里早点说
      _lanCheck?.cancel();
      final guessed = _lanHost != null && _customHost == null;
      if (guessed || _relay != null) {
        _lanCheck = Timer(Duration(seconds: _relay != null ? 10 : 5), () {
          if (!mounted || gen != _gen || _stats.video) return;
          final code = _relay != null ? 'route_unreachable' : 'lan_unreachable';
          unawaited(_enqueue(() => _applyFail(code, gen)));
        });
      }
    } on PlatformException catch (error) {
      if (!mounted || gen != _gen) return;
      await _applyFail(
        error.code == 'unsupported_codec'
            ? 'unsupported_codec'
            : 'capture_failed',
        gen,
      );
    }
  }

  Future<void> _onDeskError(Object error, int gen) async {
    if (!mounted || gen != _gen) return;
    final code = error is RpcException ? error.code : 'rpc';
    if (code == 'closed') {
      _gen++;
      await _releaseHeld();
      await _dropDesk(unsubscribe: false);
      await _stopReceiver();
      _keys = null;
      if (!mounted) return;
      setState(() {
        _up = false;
        _texture = null;
        _fail = '连接已断开';
      });
      return;
    }
    await _releaseHeld();
    await _applyFail(code, gen);
  }

  Future<void> _finishWatch(int gen, String reason) async {
    if (!mounted || gen != _gen) return;
    _gen++;
    _lockRetry?.cancel();
    _lockRetry = null;
    await _releaseHeld();
    await _dropDesk(unsubscribe: false);
    await _stopReceiver();
    _keys = null;
    _readyPort = null;
    if (!mounted) return;
    setState(() {
      _up = false;
      _texture = null;
      _fail = null;
      _macLocked = false;
      _endNote = deskEndText(reason);
      if (reason == 'stopped') _stopped = true;
    });
    if (reason == 'locked' && !_stopped && !_background) _armLockRetry();
  }

  Future<void> _applyFail(String code, int gen) async {
    if (!mounted || gen != _gen) return;
    _gen++;
    _lockRetry?.cancel();
    _lockRetry = null;
    // ready 之后失败时采集已经在跑，要退订才会停。
    await _dropDesk(unsubscribe: true);
    await _stopReceiver();
    _keys = null;
    if (!mounted) return;
    setState(() {
      _fail = deskFailText(code);
      _endNote = null;
      _up = false;
      _texture = null;
    });
    if (code == 'locked' && !_stopped && !_background) _armLockRetry();
  }

  void _armLockRetry() {
    if (_lockRetry != null) return;
    _lockRetry = Timer(const Duration(seconds: 2), () {
      _lockRetry = null;
      if (!mounted || _stopped || _background) return;
      unawaited(_ensure(force: true));
    });
  }

  Future<void> _releaseHeld() async {
    _link.release();
    await _sendTail;
    await _drainPackets();
  }

  Future<void> _dropDesk({required bool unsubscribe}) async {
    final frames = _deskFrames;
    final sub = _deskSub;
    _deskFrames = null;
    _deskSub = null;
    await frames?.cancel();
    await sub?.cancel();
    if (!unsubscribe) return;
    final rpc = _rpcNow;
    if (rpc == null) return;
    try {
      await rpc.call('desk.unsubscribe', const {});
    } on Object {
      // 连接已经没了。
    }
  }

  Future<void> _stopReceiver() async {
    _punchAsked = false;
    // 接收停了不会再来统计：留着最后一份 video 为真，页面会以为还在播，黑屏不出提示
    if (mounted) {
      _stats = const DeskStats();
      _statsTick.value = const DeskStats();
    }
    if (!deskSupported) return;
    try {
      await _receiver.stop();
    } on PlatformException {
      // 已经停过。
    }
  }

  (int, int) _screenPixels() {
    final size = MediaQuery.sizeOf(context);
    final ratio = MediaQuery.devicePixelRatioOf(context);
    var width = (size.width * ratio).round();
    var height = (size.height * ratio).round();
    if (width < 1) width = 1;
    if (height < 1) height = 1;
    return (width, height);
  }

  void _useAddress(String host, int? port) {
    final auto = _autoHost;
    final sameHost = auto != null && host == auto;
    if (sameHost && port == null) {
      _customHost = null;
      _customPort = null;
    } else {
      _customHost = sameHost ? null : host;
      _customPort = port;
    }
    unawaited(_enqueue(_retargetBody));
  }

  Future<void> _retargetBody() async {
    final keys = _keys;
    final host = _udpHost;
    final port = _customPort ?? _readyPort;
    if (!_up || keys == null || port == null) return;
    if ((host == null || host.isEmpty) && _relay == null) return;
    final gen = _gen;
    try {
      await _stopReceiver();
      if (!mounted || gen != _gen) return;
      final id = await _receiver.start(
        host: host,
        port: host == null ? null : port,
        relay: _relay,
        relayToken: _relayToken,
        keyToPhone: keys.toPhone,
        keyToMac: keys.toMac,
        codec: DeskCodec.hevc,
        nack: _nack,
      );
      if (!mounted || gen != _gen) {
        await _stopReceiver();
        return;
      }
      if (mounted) setState(() => _texture = id);
    } on PlatformException catch (error) {
      if (!mounted || gen != _gen) return;
      await _applyFail(
        error.code == 'unsupported_codec'
            ? 'unsupported_codec'
            : 'capture_failed',
        gen,
      );
    }
  }

  Duration get _now => _watch.elapsed;

  double get _aspect {
    if (_stats.width > 0 && _stats.height > 0) {
      return _stats.width / _stats.height;
    }
    return 16 / 9;
  }

  void _fitStage(double width, double height) {
    final aspect = _aspect;
    final prevW = _gestures.width;
    final prevH = _gestures.height;
    final prevAspect = _gestures.aspect;
    if (prevW == width && prevH == height && prevAspect == aspect) return;
    final from = fitContain(prevW, prevH, prevAspect);
    _gestures.resize(width, height, aspect);
    if (prevW > 1 && prevH > 1) {
      keepDesktopCenter(
        viewport: _gestures.viewport,
        from: from,
        to: _gestures.fit,
        fromView: Size(prevW, prevH),
        toView: Size(width, height),
      );
    }
  }

  void _syncTicker() {
    final need = _gestures.needsTick || _marks.isNotEmpty;
    if (need) {
      if (!_ticker.isActive) _ticker.start();
    } else if (_ticker.isActive) {
      _ticker.stop();
    }
  }

  void _pointer(void Function() body) {
    body();
    _frame.value++;
    _syncTicker();
  }

  _Mark? _mark(int pointer) {
    for (final mark in _marks) {
      if (mark.pointer == pointer) return mark;
    }
    return null;
  }

  void _down(PointerDownEvent event) {
    _pointer(() {
      final local = event.localPosition;
      _gestures.trackpad = widget.model.settings.deskTrackpad;
      // 触控板模式下点按落在指针上，不在手指下面画点
      final onPicture =
          !_gestures.trackpad && _gestures.map(local.dx, local.dy) != null;
      _gestures.down(event.pointer, local.dx, local.dy, _now);
      if (onPicture) _marks.add(_Mark(event.pointer, local));
    });
  }

  void _move(PointerMoveEvent event) {
    _pointer(() {
      final local = event.localPosition;
      _gestures.move(event.pointer, local.dx, local.dy, _now);
      final mark = _mark(event.pointer);
      if (mark == null || mark.fadeAt != null) return;
      mark.at = local;
      if (_gestures.dragging && _gestures.dragPointer == event.pointer) {
        mark.solid = true;
      }
    });
  }

  void _pointerUp(PointerUpEvent event) {
    _pointer(() {
      _gestures.up(
        event.pointer,
        event.localPosition.dx,
        event.localPosition.dy,
        _now,
      );
      final mark = _mark(event.pointer);
      if (mark == null || mark.fadeAt != null) return;
      mark.solid = false;
      mark.fadeAt = _now;
    });
  }

  void _cancel(PointerCancelEvent event) {
    _pointer(() {
      _gestures.cancel(event.pointer, _now);
      final mark = _mark(event.pointer);
      if (mark == null || mark.fadeAt != null) return;
      mark.solid = false;
      mark.fadeAt = _now;
    });
  }

  /// ⌃ 加一个键，切空间、开调度中心用。只带 ⌃，不动快捷键栏上点过、锁着的修饰键。
  void _control(String key) {
    final hid = deskKeyHid[key];
    if (hid == null) return;
    final bits = deskModBits({DeskMod.control});
    _link.batch(() {
      _link.keyDown(hid, bits);
      _link.keyUp(hid, bits);
    });
    setState(() => _log.add(DeskKeyEvent(deskChord({DeskMod.control}, key))));
    _noteInput();
  }

  void _press(String key) {
    if (deskSpaceKeys[key] case final arrow?) return _control(arrow);
    final hid = deskKeyHid[key];
    if (hid == null) return;
    final mods = _mods.consume();
    final bits = deskModBits(mods);
    _link.batch(() {
      _link.keyDown(hid, bits);
      _link.keyUp(hid, bits);
    });
    setState(() => _log.add(DeskKeyEvent(deskChord(mods, key))));
    _noteInput();
  }

  void _onTyped() {
    final value = _input.value;
    // 组字还没上屏时先留着，动了输入框的话中文输入法会被打断。
    if (value.composing.isValid && !value.composing.isCollapsed) return;
    final (deleted, text) = deskTypedDiff(_shadow, value.text);
    _shadow = value.text;
    // 攒得多了、垫着的零宽空格快删完了才重置：每次都清空会和系统输入法抢，打快了重复前缀
    if (value.text.length > 64 ||
        !value.text.startsWith(deskTypingPad.substring(2))) {
      _input.value = _padded;
      _shadow = deskTypingPad;
    }
    if (deleted == 0 && text.isEmpty) return;
    if (deleted > 0) {
      _link.batch(() {
        for (var i = 0; i < deleted; i++) {
          _link.keyDown(deskBackspaceHid, 0);
          _link.keyUp(deskBackspaceHid, 0);
        }
      });
      _log.add(DeskKeyEvent(deleted == 1 ? '⌫' : '⌫×$deleted'));
    }
    if (text.isNotEmpty && _mods.any) {
      for (final rune in text.runes) {
        final mods = _mods.consume();
        final piece = String.fromCharCodes([rune]);
        _link.text(piece, deskModBits(mods));
        _log.add(DeskKeyEvent(deskChord(mods, piece)));
      }
    } else if (text.isNotEmpty) {
      _link.text(text, 0);
      _log.add(DeskTextEvent(text));
    }
    _noteInput();
    if (mounted) setState(() {});
  }

  /// 这台电脑有没有在钥匙串里记着密码，决定胶囊上是「解锁」还是「输入密码」。
  Future<void> _checkSaved() async {
    final key = _computerKey;
    final saved = key != null && await _passwords.has(key);
    if (mounted && saved != _passwordSaved) {
      setState(() => _passwordSaved = saved);
    }
  }

  /// 锁屏时输密码：弹层里输入，可以记住或忘掉。
  Future<void> _unlock() async {
    _pokeBar();
    final key = _computerKey;
    final answer = await askMacPassword(
      context,
      canRemember: key != null,
      saved: _passwordSaved,
    );
    if (!mounted || answer == null) return;
    final password = answer.password;
    if (password == null) {
      // 只有记着密码时才有「忘掉」，这时 key 一定有
      await _passwords.forget(key!);
      if (!mounted) return;
      setState(() => _passwordSaved = false);
      _note('已忘掉记住的密码');
      return;
    }
    _sendPassword(password);
    if (key == null) return;
    if (answer.remember) {
      _pendingSave = (key: key, password: password, at: DateTime.now());
    } else if (_passwordSaved) {
      await _passwords.forget(key);
      if (mounted) setState(() => _passwordSaved = false);
    }
  }

  Future<void> _savePending() async {
    final pending = _pendingSave;
    _pendingSave = null;
    if (pending == null ||
        DateTime.now().difference(pending.at) > const Duration(seconds: 15)) {
      return;
    }
    final ok = await _passwords.save(pending.key, pending.password);
    if (!mounted) return;
    setState(() => _passwordSaved = ok);
    _note(ok ? '已记住密码，下次锁屏点「解锁」即可' : '没能记住密码');
  }

  /// 用记住的密码解锁。
  Future<void> _unlockSaved() async {
    _pokeBar();
    final key = _computerKey;
    if (key == null) return;
    final password = await _passwords.read(key);
    if (!mounted) return;
    if (password == null) {
      await _checkSaved();
      return;
    }
    _sendPassword(password);
  }

  /// 整段按键发过去再回车。密码不进事件记录（调试面板会显示）。
  void _sendPassword(String password) {
    // 输密码的工夫 Mac 在本地解开了：再发就打进了最前面的 App
    if (!_macLocked) {
      _note('Mac 已经解锁，密码没有发送');
      return;
    }
    final enter = deskKeyHid['Enter']!;
    _link.batch(() {
      _link.text(password, 0);
      _link.keyDown(enter, 0);
      _link.keyUp(enter, 0);
    });
    _noteInput();
  }

  /// 锁屏胶囊：和悬浮球一样的圆角和底色，按钮压到胶囊里面。
  Widget _lockPill(PosseColors colors) {
    final button = FilledButton.styleFrom(
      minimumSize: const Size(0, 36),
      padding: const EdgeInsets.symmetric(horizontal: 16),
      tapTargetSize: MaterialTapTargetSize.shrinkWrap,
      shape: const StadiumBorder(),
    );
    return Material(
      key: const Key('desk-locked'),
      color: colors.card.withValues(alpha: 0.94),
      borderRadius: BorderRadius.circular(999),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 4, 4, 4),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.lock_outline_rounded, size: 16, color: colors.muted),
            const SizedBox(width: 6),
            Text(
              'Mac 已锁屏',
              style: TextStyle(fontSize: Fs.meta, color: colors.text),
            ),
            const SizedBox(width: 8),
            if (_passwordSaved) ...[
              IconButton(
                key: const Key('desk-unlock-edit'),
                tooltip: '换密码或忘掉',
                visualDensity: VisualDensity.compact,
                constraints: const BoxConstraints.tightFor(
                  width: 36,
                  height: 36,
                ),
                padding: EdgeInsets.zero,
                onPressed: () => unawaited(_unlock()),
                icon: Icon(Icons.key_rounded, size: 18, color: colors.muted),
              ),
              const SizedBox(width: 2),
              FilledButton(
                key: const Key('desk-unlock'),
                style: button,
                onPressed: () => unawaited(_unlockSaved()),
                child: const Text('解锁'),
              ),
            ] else
              FilledButton(
                key: const Key('desk-unlock'),
                style: button,
                onPressed: () => unawaited(_unlock()),
                child: const Text('输入密码'),
              ),
          ],
        ),
      ),
    );
  }

  void _toggleKeyboard() {
    if (_keyboard.hasFocus) {
      _keyboard.unfocus();
    } else {
      _keyboard.requestFocus();
    }
    _pokeBar();
  }

  void _syncSystemUi() {
    final wanted = _immersive
        ? SystemUiMode.immersiveSticky
        : SystemUiMode.edgeToEdge;
    if (wanted == _ui) return;
    _ui = wanted;
    SystemChrome.setEnabledSystemUIMode(wanted);
  }

  @override
  Widget build(BuildContext context) {
    final colors = PosseColors.of(context);
    if (_ui !=
        (_immersive ? SystemUiMode.immersiveSticky : SystemUiMode.edgeToEdge)) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _syncSystemUi();
      });
    }
    final landscape =
        MediaQuery.orientationOf(context) == Orientation.landscape;
    final keys = _chrome.keysVisible(_keyboardUp);
    // 键盘和快捷键栏盖在画面上，画面不跟着缩：缩了整张桌面变小，字更难看清
    final keyboard = MediaQuery.viewInsetsOf(context).bottom;
    final screen = MediaQuery.sizeOf(context);
    final covered =
        keyboard +
        (keys
            ? (landscape ? deskKeyBarDense : deskKeyBarBody) +
                  1 +
                  MediaQuery.paddingOf(context).bottom
            : 0);
    final lift = deskLift(
      tapY: keyboard > 0 ? _typeY : null,
      screenHeight: screen.height,
      covered: covered,
    );
    return Scaffold(
      backgroundColor: deskLetterbox,
      resizeToAvoidBottomInset: false,
      body: Stack(
        children: [
          Positioned.fill(child: _stage(colors, landscape, lift, covered)),
          Positioned(
            left: 0,
            right: 0,
            bottom: keyboard,
            child: AnimatedSize(
              duration: const Duration(milliseconds: 160),
              curve: Curves.easeOutCubic,
              alignment: Alignment.bottomCenter,
              child: keys
                  ? DeskKeyBar(
                      mods: _mods,
                      keyboardUp: _keyboardUp,
                      dense: landscape,
                      onKeyboard: _toggleKeyboard,
                      onKey: _press,
                      onModTap: (mod) => setState(() => _mods.tap(mod)),
                      onModLock: (mod) => setState(() => _mods.lock(mod)),
                    )
                  : const SizedBox.shrink(),
            ),
          ),
        ],
      ),
    );
  }

  Widget _stage(
    PosseColors colors,
    bool landscape,
    double lift,
    double covered,
  ) {
    final banner = _banner();
    // 状态栏藏起来之后 padding 会变 0，刘海还在，用 viewPadding 把胶囊让开。
    final pad = MediaQuery.viewPaddingOf(context);
    final picturePad = deskPicturePadding(
      landscape: landscape,
      viewPadding: pad,
    );
    final top = pad.top < 8 ? 8.0 : pad.top;
    return ClipRect(
      child: Stack(
        fit: StackFit.expand,
        children: [
          const ColoredBox(color: deskLetterbox),
          TweenAnimationBuilder<double>(
            tween: Tween(end: lift),
            duration: const Duration(milliseconds: 220),
            curve: Curves.easeOutCubic,
            builder: (context, dy, child) =>
                Transform.translate(offset: Offset(0, -dy), child: child),
            child: Padding(
              padding: picturePad,
              child: LayoutBuilder(
                builder: (context, constraints) {
                  _fitStage(constraints.maxWidth, constraints.maxHeight);
                  return Stack(
                    fit: StackFit.expand,
                    children: [
                      AnimatedBuilder(
                        animation: _frame,
                        builder: (context, _) =>
                            _pictureLayer(colors, constraints.biggest),
                      ),
                      Listener(
                        behavior: HitTestBehavior.opaque,
                        onPointerDown: _down,
                        onPointerMove: _move,
                        onPointerUp: _pointerUp,
                        onPointerCancel: _cancel,
                        child: const SizedBox.expand(),
                      ),
                      // 只有卡片本身接点击，旁边的手势照常到画面
                      if (banner != null)
                        Center(child: _bannerView(colors, banner)),
                    ],
                  );
                },
              ),
            ),
          ),
          // 和悬浮球同一高度，左右让出球的宽度，球吸在哪边都不压着
          if (_macLocked && _stats.video && !_background)
            Positioned(
              top: top,
              left: 64 + pad.left,
              right: 64 + pad.right,
              child: Align(
                alignment: Alignment.topCenter,
                child: _lockPill(colors),
              ),
            ),
          if (_debugOpen)
            Positioned(
              top: top,
              // 两边让出悬浮球的宽度，它吸在哪边都不压着面板
              left: 64 + pad.left,
              right: 64 + pad.right,
              child: ValueListenableBuilder<DeskStats>(
                valueListenable: _statsTick,
                builder: (context, stats, _) => Align(
                  alignment: Alignment.topCenter,
                  child: _debugPanel(colors, stats),
                ),
              ),
            ),
          Positioned(
            top: top,
            left: pad.left + 4,
            right: pad.right + 4,
            // 键盘和快捷键栏弹出时球留在它们上面
            bottom: math.max(pad.bottom < 8 ? 8.0 : pad.bottom, covered + 8),
            child: ListenableBuilder(
              listenable: widget.model,
              builder: (context, _) {
                return ValueListenableBuilder<DeskStats>(
                  valueListenable: _statsTick,
                  builder: (context, stats, _) => DeskBarSlot(
                    anchor: landscape ? _sideAnchor(pad) : _anchorTop,
                    onDrag: _dragBar,
                    child: DeskToolbar(
                      expanded: _chrome.expanded,
                      latency: widget.model.latency,
                      unresponsive: widget.model.unresponsive,
                      fps: stats.fps,
                      still: stats.still,
                      video: stats.video,
                      landscape: landscape,
                      keyboardUp: _keyboardUp,
                      shortcuts: _chrome.shortcuts,
                      debugOpen: _debugOpen,
                      stopped: _stopped,
                      onToggle: _toggleBar,
                      onBack: () => Navigator.maybePop(context),
                      onOrientation: _toggleOrientation,
                      onKeyboard: _toggleKeyboard,
                      onShortcuts: _toggleShortcuts,
                      onDebug: _toggleDebug,
                      onHelp: _showHint,
                      onDisconnect: () => unawaited(_disconnect()),
                      onView: _displays.list.isNotEmpty
                          ? () => unawaited(_pickView())
                          : null,
                    ),
                  ),
                );
              },
            ),
          ),
          Positioned(
            left: -200,
            top: 0,
            width: 80,
            height: 32,
            child: TextField(
              focusNode: _keyboard,
              controller: _input,
              autocorrect: false,
              enableSuggestions: false,
              decoration: const InputDecoration(border: InputBorder.none),
              onSubmitted: (_) {
                _press('Enter');
                _keyboard.requestFocus();
              },
            ),
          ),
          if (_hintOpen || !widget.model.settings.deskGesturesSeen)
            Positioned.fill(
              child: DeskGestureHint(
                onDismiss: _dismissHint,
                trackpad: widget.model.settings.deskTrackpad,
              ),
            ),
        ],
      ),
    );
  }

  /// 手指移动和惯性只重建这一层。顶栏、快捷键栏、调试浮层不在这里。
  Widget _pictureLayer(PosseColors colors, Size size) {
    final frame = fitContain(size.width, size.height, _aspect);
    final image = deskImageRect(frame, _gestures.viewport);
    return Stack(
      fit: StackFit.expand,
      children: [
        const ColoredBox(color: deskLetterbox),
        Positioned(
          left: image.left,
          top: image.top,
          width: image.width,
          height: image.height,
          child: const ColoredBox(color: _picture),
        ),
        if (_texture != null)
          Positioned(
            left: image.left,
            top: image.top,
            width: image.width,
            height: image.height,
            child: _stats.view
                ? const DeskSurfaceView()
                : Texture(textureId: _texture!),
          ),
        for (final mark in _marks) _dot(colors, mark),
        if (widget.model.settings.deskTrackpad && _texture != null)
          _cursorRing(colors),
      ],
    );
  }

  (bool waiting, String text)? _banner() {
    if (_background) return (false, '已暂停（后台）');
    if (_viewNote != null) return (false, _viewNote!);
    if (_accessNote != null && (_stats.video || _up)) {
      return (false, _accessNote!);
    }
    if (_stats.video) return null;
    if (_indirect) return (false, deskIndirectText);
    if (_fail != null) return (false, '连不上：$_fail');
    if (_endNote != null) return (false, _endNote!);
    if (_stopped) return (false, '已断开');
    final host = _autoHost;
    if (host == null || host.isEmpty) return (false, '连不上：没有连着的电脑');
    if (!_up) return (true, '正在连接');
    return (true, '已连上，等画面');
  }

  Widget _bannerView(PosseColors colors, (bool waiting, String text) banner) {
    final (waiting, text) = banner;
    if (waiting) {
      return IgnorePointer(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            PosseLoader(size: 36, color: colors.accent),
            const SizedBox(height: 14),
            Text(
              text,
              textAlign: TextAlign.center,
              style: const TextStyle(fontSize: Fs.body, color: _onPicture),
            ),
          ],
        ),
      );
    }
    final failed = text.startsWith('连不上') || text == deskIndirectText;
    // 有画面时的提示（缺辅助功能）只是一行字，不挡操作
    if (_stats.video) {
      return IgnorePointer(
        child: Text(
          text,
          textAlign: TextAlign.center,
          style: const TextStyle(fontSize: Fs.body, color: _onPicture),
        ),
      );
    }
    final retry =
        !_background && (_fail != null || _endNote != null || _stopped);
    final hint = _fail == deskFailText('permission_screen')
        ? 'Mac 上打开「系统设置 › 隐私与安全性 › 录屏与系统录音」里的 Posse，再重试'
        : null;
    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 340),
      child: Material(
        key: const Key('desk-banner'),
        color: colors.card.withValues(alpha: 0.94),
        borderRadius: BorderRadius.circular(Rd.card),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(20, 18, 20, 16),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(
                failed
                    ? Icons.error_outline_rounded
                    : Icons.desktop_access_disabled_outlined,
                size: 28,
                color: failed ? colors.danger : colors.muted,
              ),
              const SizedBox(height: 10),
              Text(
                text.replaceFirst('连不上：', ''),
                textAlign: TextAlign.center,
                style: TextStyle(
                  fontSize: Fs.body,
                  fontWeight: FontWeight.w500,
                  color: colors.text,
                ),
              ),
              if (hint != null) ...[
                const SizedBox(height: 6),
                Text(
                  hint,
                  textAlign: TextAlign.center,
                  style: TextStyle(fontSize: Fs.meta, color: colors.muted),
                ),
              ],
              if (retry) ...[
                const SizedBox(height: 14),
                FilledButton.icon(
                  key: const Key('desk-retry'),
                  onPressed: () {
                    _pokeBar();
                    unawaited(_ensure(force: true));
                  },
                  icon: const Icon(Icons.refresh_rounded, size: 18),
                  label: Text(_stopped ? '重新连接' : '重试'),
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  /// 触控板模式下手机认为的指针位置，套在画面里 Mac 指针的尖上。
  /// 画面里的指针要等一个来回才动，这个圈跟手。
  Widget _cursorRing(PosseColors colors) {
    final at = _gestures.cursorView;
    final color = colors.accent.withValues(alpha: 0.85);
    // 实心是左键按着，空心是只移指针；松手等续拖时实心慢慢变淡，淡完松开
    final left = _gestures.graceLeft(_now);
    return Positioned(
      left: at.dx - _cursorRingSize / 2,
      top: at.dy - _cursorRingSize / 2,
      child: IgnorePointer(
        child: Container(
          key: const Key('desk-cursor-ring'),
          width: _cursorRingSize,
          height: _cursorRingSize,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            color: !_gestures.buttonHeld
                ? null
                : left == null
                ? color
                : color.withValues(alpha: 0.85 * left),
            border: Border.all(color: color, width: 1.5),
          ),
        ),
      ),
    );
  }

  Widget _dot(PosseColors colors, _Mark mark) {
    final age = mark.fadeAt == null ? Duration.zero : _now - mark.fadeAt!;
    final opacity = mark.fadeAt == null
        ? 1.0
        : (1 - age.inMilliseconds / _markFade.inMilliseconds).clamp(0.0, 1.0);
    final color = colors.accent;
    return Positioned(
      left: mark.at.dx - _markSize / 2,
      top: mark.at.dy - _markSize / 2,
      child: IgnorePointer(
        child: Opacity(
          opacity: opacity,
          child: Container(
            width: _markSize,
            height: _markSize,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: mark.solid ? color : color.withValues(alpha: 0.28),
              border: Border.all(color: color, width: mark.solid ? 0 : 1.5),
            ),
          ),
        ),
      ),
    );
  }

  Widget _debugPanel(PosseColors colors, DeskStats stats) {
    final view = _gestures.viewport;
    final headline =
        '${deskFpsLabel(stats.fps, video: stats.video, still: stats.still)}'
        '  收到 ${stats.received.toStringAsFixed(0)} fps';
    final rate =
        '码率 ${deskBitrate(stats.bitrate)}（${deskByteRate(stats.bitrate)}）'
        '  峰值 ${deskBitrate(stats.peak)}';
    final cap = MediaQuery.sizeOf(context).height * 0.45;
    return ConstrainedBox(
      constraints: BoxConstraints(maxWidth: 260, maxHeight: cap),
      child: Material(
        key: const Key('desk-debug-panel'),
        color: colors.card.withValues(alpha: 0.92),
        borderRadius: BorderRadius.circular(Rd.card),
        clipBehavior: Clip.antiAlias,
        child: SingleChildScrollView(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 4),
          child: DefaultTextStyle(
            style: statusLineStyle(colors.text),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(headline),
                Text(rate),
                Text('丢包 ${stats.loss}'),
                Text('线路 ${deskPathLabel(stats.path)}'),
                ValueListenableBuilder<(int, int)>(
                  valueListenable: _inputLine,
                  builder: (context, line, _) =>
                      Text('输入：已发 ${line.$1}、已确认 ${line.$2}'),
                ),
                Text('解码 ${stats.decodeMs.toStringAsFixed(1)} ms'),
                Text('关键帧 ${stats.recoveries}'),
                Text(
                  '缩放 ${view.zoom.toStringAsFixed(2)}'
                  '  平移 ${view.panX.toStringAsFixed(2)},'
                  ' ${view.panY.toStringAsFixed(2)}',
                ),
                for (final event in _log.events)
                  Text(
                    deskEventLine(event),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    style: TextButton.styleFrom(
                      foregroundColor: colors.accent,
                      backgroundColor: Colors.transparent,
                      minimumSize: const Size(0, 32),
                      padding: const EdgeInsets.symmetric(horizontal: 4),
                      textStyle: statusLineStyle(colors.accent),
                    ),
                    onPressed: () => unawaited(_connectSheet()),
                    icon: const Icon(Icons.tune_rounded, size: 16),
                    label: const Text('UDP 地址'),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Future<void> _connectSheet() async {
    final host = TextEditingController(text: _udpHost ?? '');
    final port = TextEditingController(
      text: _customPort == null ? '' : '$_customPort',
    );
    final auto = _autoHost;
    var error = '';
    try {
      await showModalBottomSheet<void>(
        context: context,
        showDragHandle: true,
        isScrollControlled: true,
        builder: (sheet) {
          final c = PosseColors.of(sheet);
          final bottom = MediaQuery.viewInsetsOf(sheet).bottom;
          return StatefulBuilder(
            builder: (context, setSheet) {
              void apply() {
                final nextHost = host.text.trim();
                final portText = port.text.trim();
                if (nextHost.isEmpty) {
                  setSheet(() => error = '先填地址');
                  return;
                }
                int? nextPort;
                if (portText.isNotEmpty) {
                  nextPort = parseDeskPort(portText);
                  if (nextPort == null) {
                    setSheet(() => error = '端口要在 1 到 65535');
                    return;
                  }
                }
                Navigator.of(sheet).pop();
                _useAddress(nextHost, nextPort);
              }

              return Padding(
                padding: EdgeInsets.fromLTRB(16, 0, 16, 16 + bottom),
                child: SafeArea(
                  top: false,
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Text(
                        '连接设置',
                        style: TextStyle(
                          fontSize: Fs.cardTitle,
                          fontWeight: FontWeight.w600,
                          color: c.text,
                        ),
                      ),
                      const SizedBox(height: 6),
                      Text(
                        auto == null || auto.isEmpty
                            ? '没有连着的电脑'
                            : '默认走订阅。画面发到现在连着的电脑，端口由电脑分配。这里改的是 UDP 地址，留着调试。',
                        style: TextStyle(fontSize: Fs.meta, color: c.muted),
                      ),
                      const SizedBox(height: 14),
                      Row(
                        children: [
                          Expanded(
                            flex: 3,
                            child: _field(c, host, '主机', apply),
                          ),
                          const SizedBox(width: 8),
                          Expanded(child: _field(c, port, '端口', apply)),
                        ],
                      ),
                      if (error.isNotEmpty) ...[
                        const SizedBox(height: 8),
                        Text(
                          error,
                          style: TextStyle(fontSize: Fs.meta, color: c.danger),
                        ),
                      ],
                      const SizedBox(height: 12),
                      Align(
                        alignment: Alignment.centerRight,
                        child: TextButton(
                          onPressed: apply,
                          child: const Text('使用'),
                        ),
                      ),
                      if (_custom)
                        Align(
                          alignment: Alignment.centerLeft,
                          child: TextButton(
                            onPressed: () {
                              Navigator.of(sheet).pop();
                              _customHost = null;
                              _customPort = null;
                              unawaited(_enqueue(_retargetBody));
                              setState(() {});
                            },
                            child: const Text('恢复默认'),
                          ),
                        ),
                    ],
                  ),
                ),
              );
            },
          );
        },
      );
    } finally {
      // 弹层关掉的这一帧还在用控制器，等画完再释放。
      WidgetsBinding.instance.addPostFrameCallback((_) {
        host.dispose();
        port.dispose();
      });
    }
  }

  Widget _field(
    PosseColors colors,
    TextEditingController controller,
    String hint,
    VoidCallback onSubmitted,
  ) {
    return TextField(
      controller: controller,
      keyboardType: hint == '端口' ? TextInputType.number : TextInputType.url,
      textInputAction: TextInputAction.done,
      style: mono(13, color: colors.text),
      cursorColor: colors.accent,
      decoration: InputDecoration(
        isDense: true,
        hintText: hint,
        hintStyle: TextStyle(color: colors.faint, fontSize: 13),
        filled: true,
        fillColor: colors.fill,
        contentPadding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(Rd.button),
          borderSide: BorderSide.none,
        ),
      ),
      onSubmitted: (_) => onSubmitted(),
    );
  }
}

int? _jsonInt(Object? value) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  return null;
}

extension on DeskGestures {
  DeskPoint? map(double x, double y) =>
      mapDesktop(x: x, y: y, fit: fit, viewport: viewport);
}

/// 安卓的平台视图画面（[DeskStats.view]）。只显示，手势照旧由外层收。
class DeskSurfaceView extends StatelessWidget {
  const DeskSurfaceView({super.key});

  @override
  Widget build(BuildContext context) {
    return IgnorePointer(
      child: PlatformViewLink(
        viewType: 'posse/desk/view',
        surfaceFactory: (context, controller) => AndroidViewSurface(
          controller: controller as AndroidViewController,
          gestureRecognizers: const {},
          hitTestBehavior: PlatformViewHitTestBehavior.transparent,
        ),
        onCreatePlatformView: (params) {
          // SurfaceView 要混合合成，initSurfaceAndroidView 会在需要时退到它
          return PlatformViewsService.initSurfaceAndroidView(
              id: params.id,
              viewType: 'posse/desk/view',
              layoutDirection: TextDirection.ltr,
              creationParamsCodec: const StandardMessageCodec(),
            )
            ..addOnPlatformViewCreatedListener(params.onPlatformViewCreated)
            ..create();
        },
      ),
    );
  }
}
