import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import 'chrome.dart';
import 'events.dart';
import 'gestures.dart';
import 'hint.dart';
import 'key_bar.dart';
import 'keys.dart';
import 'mods.dart';
import 'plugin.dart';
import 'toolbar.dart';
import 'viewport.dart';

/// 留空和画面要能分开：画面是黑的，留空用很深的暖灰。
const deskLetterbox = Color(0xFF2A2926);
const _picture = Color(0xFF000000);
const _onPicture = Color(0xFFC8C6C1);

const _markSize = 22.0;
const _markFade = Duration(milliseconds: 280);

/// 左缘拖动是画面平移，Listener 不参与手势竞争，和页面返回会一起发生。
/// 返回放在工具条上。
class DeskRoute extends MaterialPageRoute<void> {
  DeskRoute({required super.builder});

  @override
  bool get popGestureEnabled => false;
}

/// 看 Mac 桌面。订阅协议第 4 步再接，这一步用固定密钥直接收 UDP。
class DeskPage extends StatefulWidget {
  const DeskPage({super.key, required this.model, this.receiver});

  final HiveAppModel model;
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

class _DeskPageState extends State<DeskPage>
    with WidgetsBindingObserver, SingleTickerProviderStateMixin {
  late final DeskReceiver _receiver = widget.receiver ?? DeskReceiver();
  late final DeskGestures _gestures;
  final _log = DeskEventLog();
  final _mods = DeskMods();
  final _watch = Stopwatch();
  final _marks = <_Mark>[];
  final _keyboard = FocusNode();
  final _input = TextEditingController();
  final _chrome = DeskChrome();
  final _edges = DeskEdges();
  final _lease = Object();
  final _anchorTop = ValueNotifier<double>(0.5);
  final _anchorSide = ValueNotifier<double>(0.5);
  final _zoomed = ValueNotifier<bool>(false);

  StreamSubscription<DeskStats>? _statsSub;
  Timer? _barTimer;
  late final Ticker _ticker;
  final _frame = ValueNotifier<int>(0);
  final _statsTick = ValueNotifier<DeskStats>(const DeskStats());
  DeskStats _stats = const DeskStats();
  int? _texture;
  String? _fail;
  String? _customHost;
  int? _customPort;
  String? _hostNow;
  int? _portNow;
  var _up = false;
  var _background = false;
  var _debugOpen = false;
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
    _keyboard.addListener(_keyboardChanged);
    _input.addListener(_onTyped);
    if (Platform.isIOS) {
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
    _ticker.dispose();
    _frame.dispose();
    _statsTick.dispose();
    _anchorTop.dispose();
    _anchorSide.dispose();
    _zoomed.dispose();
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
    if (Platform.isIOS) unawaited(_receiver.stop());
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

  String? get _host {
    final custom = _customHost;
    if (custom != null && custom.isNotEmpty) return custom;
    return _autoHost;
  }

  int get _port => _customPort ?? deskDefaultPort;

  bool get _custom {
    if (_customPort != null && _customPort != deskDefaultPort) return true;
    final custom = _customHost;
    return custom != null && custom.isNotEmpty && custom != _autoHost;
  }

  String? get _where {
    final host = _host;
    if (host == null || host.isEmpty) return null;
    return deskEndpoint(host, _port);
  }

  bool get _immersive => _stats.video && !_background;

  void _onGesture(DeskEvent event) {
    if (deskHapticFor(event)) unawaited(HapticFeedback.lightImpact());
    _log.add(event);
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

  void _dragBar(double delta) {
    final side = MediaQuery.orientationOf(context) == Orientation.landscape;
    final anchor = side ? _anchorSide : _anchorTop;
    anchor.value = (anchor.value + delta).clamp(0.0, 1.0);
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

  void _dismissHint() {
    widget.model.updateSettings((s) => s.deskGesturesSeen = true);
    setState(() {});
  }

  Future<void> _disconnect() async {
    _pokeBar();
    if (_stopped) {
      unawaited(_ensure(force: true));
      return;
    }
    _stopped = true;
    final gen = ++_gen;
    _stats = const DeskStats();
    _statsTick.value = const DeskStats();
    setState(() {
      _fail = null;
      _up = false;
      _texture = null;
    });
    if (!Platform.isIOS) return;
    try {
      await _receiver.stop();
    } on PlatformException {
      // 已经停过。
    }
    if (!mounted || gen != _gen) return;
  }

  void _followComputer() {
    if (!mounted || _custom) return;
    final host = _autoHost;
    if (host == null || host.isEmpty || host == _hostNow) return;
    unawaited(_ensure());
  }

  void _keyboardChanged() {
    final up = _keyboard.hasFocus;
    if (!mounted || up == _keyboardUp) return;
    setState(() => _keyboardUp = up);
  }

  void _setBackground(bool background) {
    _background = background;
    if (Platform.isIOS && _up) {
      if (background) {
        unawaited(_receiver.pause());
      } else {
        unawaited(_receiver.resume());
      }
    }
    if (mounted) setState(() {});
  }

  /// 帧率和码率只刷新胶囊。出画面、画面尺寸变了才重建整页（顶栏和比例）。
  void _onStats(DeskStats stats) {
    if (!mounted) return;
    final relayout =
        stats.video != _stats.video ||
        stats.width != _stats.width ||
        stats.height != _stats.height;
    _stats = stats;
    _statsTick.value = stats;
    if (relayout) setState(() {});
  }

  /// 页面开着就一直收。hello 由原生层发到第一帧视频为止，Mac 那边何时开始推流都行。
  Future<void> _ensure({bool force = false}) async {
    if (_stopped && !force) return;
    if (force) _stopped = false;
    final host = _host;
    final port = _port;
    if (host == null || host.isEmpty) {
      if (!mounted) return;
      setState(() {
        _fail = null;
        _up = false;
        _texture = null;
      });
      return;
    }
    if (!force &&
        _up &&
        _fail == null &&
        host == _hostNow &&
        port == _portNow) {
      return;
    }
    final gen = ++_gen;
    _hostNow = host;
    _portNow = port;
    _stats = const DeskStats();
    _statsTick.value = const DeskStats();
    setState(() {
      _fail = null;
      _up = false;
      _texture = null;
    });
    if (!Platform.isIOS) {
      setState(() => _fail = '看桌面只在 iPhone 上');
      return;
    }
    try {
      await _receiver.stop();
      if (!mounted || gen != _gen) return;
      final id = await _receiver.start(host: host, port: port);
      if (!mounted || gen != _gen) {
        await _receiver.stop();
        return;
      }
      setState(() {
        _texture = id;
        _up = true;
      });
      if (_background) unawaited(_receiver.pause());
    } on PlatformException catch (error) {
      if (!mounted || gen != _gen) return;
      setState(() => _fail = error.message ?? '连接失败');
    }
  }

  void _useAddress(String host, int port) {
    final auto = _autoHost;
    if (auto != null && host == auto && port == deskDefaultPort) {
      _customHost = null;
      _customPort = null;
    } else {
      _customHost = host;
      _customPort = port;
    }
    unawaited(_ensure(force: true));
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
    final zoomed = _gestures.viewport.zoom > deskMinZoom + 0.01;
    if (_zoomed.value != zoomed) _zoomed.value = zoomed;
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
      final onPicture = _gestures.map(local.dx, local.dy) != null;
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

  void _press(String key) {
    setState(() => _log.add(DeskKeyEvent(deskChord(_mods.consume(), key))));
  }

  void _onTyped() {
    final value = _input.value;
    // 组字还没上屏时先留着，清掉的话中文输入法会被打断。
    if (value.composing.isValid && !value.composing.isCollapsed) return;
    final text = value.text;
    if (text.isEmpty) return;
    _input.value = const TextEditingValue();
    setState(() {
      if (_mods.any) {
        for (final rune in text.runes) {
          _log.add(
            DeskKeyEvent(
              deskChord(_mods.consume(), String.fromCharCodes([rune])),
            ),
          );
        }
      } else {
        _log.add(DeskTextEvent(text));
      }
    });
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
    final colors = HiveColors.of(context);
    if (_ui !=
        (_immersive ? SystemUiMode.immersiveSticky : SystemUiMode.edgeToEdge)) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _syncSystemUi();
      });
    }
    final landscape =
        MediaQuery.orientationOf(context) == Orientation.landscape;
    final keys = _chrome.keysVisible(_keyboardUp);
    // 快捷键栏贴在系统键盘上方，页面跟着键盘缩短，不把键盖住。
    return Scaffold(
      backgroundColor: deskLetterbox,
      resizeToAvoidBottomInset: true,
      body: Column(
        children: [
          Expanded(child: _stage(colors, landscape)),
          AnimatedSize(
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
        ],
      ),
    );
  }

  Widget _stage(HiveColors colors, bool landscape) {
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
          Padding(
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
                    if (banner != null)
                      IgnorePointer(
                        child: Center(child: _bannerView(colors, banner)),
                      ),
                  ],
                );
              },
            ),
          ),
          if (_debugOpen)
            Positioned(
              top: landscape ? top : top + deskToolbarRow + 8,
              left: 16 + pad.left,
              right: 16 + pad.right,
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
            left: pad.left,
            right: pad.right,
            bottom: landscape ? (pad.bottom < 8 ? 8.0 : pad.bottom) : null,
            child: ListenableBuilder(
              listenable: widget.model,
              builder: (context, _) {
                return ValueListenableBuilder<DeskStats>(
                  valueListenable: _statsTick,
                  builder: (context, stats, _) => DeskBarSlot(
                    anchor: landscape ? _anchorSide : _anchorTop,
                    vertical: landscape,
                    onRight: deskBarOnRight(left: pad.left, right: pad.right),
                    onDrag: _dragBar,
                    child: DeskToolbar(
                      expanded: _chrome.expanded,
                      latency: widget.model.latency,
                      unresponsive: widget.model.unresponsive,
                      fps: stats.fps,
                      video: stats.video,
                      landscape: landscape,
                      keyboardUp: _keyboardUp,
                      shortcuts: _chrome.shortcuts,
                      zoomed: _zoomed,
                      debugOpen: _debugOpen,
                      stopped: _stopped,
                      onToggle: _toggleBar,
                      onBack: () => Navigator.maybePop(context),
                      onOrientation: _toggleOrientation,
                      onKeyboard: _toggleKeyboard,
                      onShortcuts: _toggleShortcuts,
                      onZoom: _resetZoom,
                      onDebug: _toggleDebug,
                      onDisconnect: () => unawaited(_disconnect()),
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
          if (!widget.model.settings.deskGesturesSeen)
            Positioned.fill(child: DeskGestureHint(onDismiss: _dismissHint)),
        ],
      ),
    );
  }

  /// 手指移动和惯性只重建这一层。顶栏、快捷键栏、调试浮层不在这里。
  Widget _pictureLayer(HiveColors colors, Size size) {
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
            child: Texture(textureId: _texture!),
          ),
        for (final mark in _marks) _dot(colors, mark),
      ],
    );
  }

  (bool waiting, String text)? _banner() {
    if (_background && _up) return (false, '已暂停（后台）');
    if (_stats.video) return null;
    if (_stopped) return (false, '已断开');
    if (_fail != null) return (false, '连不上：$_fail');
    final where = _where;
    if (!_up) {
      if (where == null) return (false, '连不上：没有连着的电脑');
      return (true, '正在连接 $where');
    }
    return (true, '已连上，等电脑推流');
  }

  Widget _bannerView(HiveColors colors, (bool waiting, String text) banner) {
    final failed = banner.$2.startsWith('连不上');
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 32),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (banner.$1) ...[
            HiveLoader(size: 36, color: colors.accent),
            const SizedBox(height: 14),
          ],
          Text(
            banner.$2,
            textAlign: TextAlign.center,
            style: TextStyle(
              fontSize: Fs.body,
              color: failed ? colors.danger : _onPicture,
            ),
          ),
        ],
      ),
    );
  }

  Widget _dot(HiveColors colors, _Mark mark) {
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

  Widget _debugPanel(HiveColors colors, DeskStats stats) {
    final view = _gestures.viewport;
    final headline =
        '${stats.fps.toStringAsFixed(0)} fps  ${deskBitrate(stats.bitrate)}';
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
                Text('丢包 ${stats.loss}'),
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
                  child: TextButton(
                    style: TextButton.styleFrom(
                      foregroundColor: colors.accent,
                      visualDensity: VisualDensity.compact,
                      padding: EdgeInsets.zero,
                    ),
                    onPressed: () => unawaited(_connectSheet()),
                    child: const Text('连接'),
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
    final host = TextEditingController(text: _host ?? '');
    final port = TextEditingController(text: '$_port');
    final auto = _autoHost;
    var error = '';
    try {
      await showModalBottomSheet<void>(
        context: context,
        showDragHandle: true,
        isScrollControlled: true,
        builder: (sheet) {
          final c = HiveColors.of(sheet);
          final bottom = MediaQuery.viewInsetsOf(sheet).bottom;
          return StatefulBuilder(
            builder: (context, setSheet) {
              void apply() {
                final nextHost = host.text.trim();
                final nextPort = parseDeskPort(port.text);
                if (nextHost.isEmpty || nextPort == null) {
                  setSheet(
                    () => error = nextPort == null ? '端口要在 1 到 65535' : '先填地址',
                  );
                  return;
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
                            ? '没有连着的电脑，填一个局域网地址'
                            : '默认用现在连着的电脑 ${deskEndpoint(auto, deskDefaultPort)}',
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
                              setState(() {
                                _customHost = null;
                                _customPort = null;
                              });
                              unawaited(_ensure(force: true));
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
    HiveColors colors,
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

extension on DeskGestures {
  DeskPoint? map(double x, double y) =>
      mapDesktop(x: x, y: y, fit: fit, viewport: viewport);
}
