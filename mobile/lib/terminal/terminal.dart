import 'dart:async';
import 'dart:convert';

import 'package:flterm/flterm.dart' hide TerminalScreen;
// ignore: implementation_imports
import 'package:flterm/src/foundation.dart' show CellMetrics;
// ignore: implementation_imports
import 'package:flterm/src/rendering.dart' show measureCellMetrics;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../chat/chat.dart';
import '../chat/clipboard_paste.dart';
import '../chat/image_pick.dart';
import '../chat/image_upload.dart';
import '../chat/send.dart';
import '../files/files.dart';
import '../l10n/l10n.dart';
import '../workspace/session_actions.dart';
import '../protocol/rpc.dart';
import '../settings/custom_keys.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import 'desk_fit.dart';
import 'selection_chrome.dart';
import 'frames.dart';
import 'keyboard_lift.dart';
import 'link_target.dart';
import '../chat/media.dart';
import '../chat/tools.dart';

/// 完全复位（RIS）并清掉回滚。
final _resetBytes = Uint8List.fromList('\x1bc\x1b[3J'.codeUnits);

/// 终端视图。单测不要 import 这个文件：flterm 带原生库，宿主 VM 测试不该编它。
class TerminalScreen extends StatefulWidget {
  const TerminalScreen({
    super.key,
    required this.model,
    required this.tab,
    this.onOpenChat,
  });

  final PosseAppModel model;
  final SessionTab tab;

  /// 设置里选了先开终端时，从这里切到对话页。
  final VoidCallback? onOpenChat;

  @override
  State<TerminalScreen> createState() => _TerminalScreenState();
}

class _TerminalScreenState extends State<TerminalScreen>
    with WidgetsBindingObserver {
  final _asm = TermAssembler();
  late final TerminalController _controller;
  StreamSubscription<Uint8List>? _binary;
  RpcStream? _stream;
  PhoneRpc? _bound;
  var _cols = 0;
  var _rows = 0;
  final _resizes = ResizeGate();
  var _heldCols = 0;
  var _heldRows = 0;

  /// 本地终端里已经写过远端的快照。之前是空白，盖一个加载动画。
  var _painted = false;

  /// 当前订阅是不是按电脑布局订的（没带手机尺寸），以及电脑那边的列数、行数。
  var _heldDesk = false;
  var _deskCols = 0;
  var _deskRows = 0;

  bool get _desk => widget.model.settings.deskLayout;

  /// 电脑布局的画面：平移、捏合中的临时倍数。静止时字号已经按吸定的倍数排过，这里的倍数是 1。
  final _deskKey = GlobalKey();

  /// 电脑布局和手机布局外面包的层级不同。没有固定的 key，换布局时会新建一个终端视图、
  /// 帧末再销毁旧的；旧视图销毁时把控制器解绑（滚动、焦点、输入法），新视图刚接上就被断开。
  final _viewKey = GlobalKey();
  final _deskScroll = TerminalScrollController();
  var _panX = 0.0;
  var _panY = 0.0;
  var _pinch = 1.0;

  /// 平移和捏合中只重画这一层，不重建整个终端页。选区的复制按钮和把手也听它。
  final _pose = ValueNotifier<int>(0);

  /// 终端画面里到第几行为止有内容，弹键盘时按它算往上挪多少。
  final _contentRows = ValueNotifier<int>(0);
  late final Listenable _moving = Listenable.merge([_pose, _contentRows]);
  var _pinching = false;
  var _pinchScale0 = 1.0;
  Offset _focalContent = Offset.zero;
  var _applying = false;
  var _gen = 0;
  Future<void> _gate = Future<void>.value();
  String? _notice;

  /// 正在上传的图片传了多少，0 到 1；没在传时为空。
  double? _upload;
  // 正在传的是文件（快捷键栏的发送文件）还是图片（剪贴板）
  var _uploadFile = false;

  Timer? _noticeTimer;

  /// 一次性的提示，[noticeLife] 后自己消失；期间换了别的提示就不动它。
  void _flash(String? text) {
    if (!mounted) return;
    _noticeTimer?.cancel();
    setState(() => _notice = text);
    if (text == null) return;
    _noticeTimer = Timer(noticeLife, () {
      if (mounted && _notice == text) setState(() => _notice = null);
    });
  }

  final _focus = FocusNode();
  var _busy = false;

  /// 点终端、点快捷键栏的键盘键都走这里：键盘弹着就放下，收着就弹出来。
  void _toggleKeyboard() {
    if (MediaQuery.viewInsetsOf(context).bottom > 0) {
      _focus.unfocus();
      return;
    }
    _showKeyboard();
  }

  /// 点终端只弹键盘、不收：输入框就在终端里，键盘弹着时点一下多半是在挪光标、点选项。收起用快捷键栏的键盘键。
  void _showKeyboard() {
    if (MediaQuery.viewInsetsOf(context).bottom > 0) return;
    _focus.requestFocus();
    // 已经有焦点但键盘被收起时，requestFocus 不会再弹，要让输入法重新出来
    SystemChannels.textInput.invokeMethod<void>('TextInput.show');
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _controller = TerminalController()
      ..onOutput = _onOutput
      ..onResize = _onResize;
    widget.model.addListener(_onModel);
  }

  Animation<double>? _entry;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final entry = ModalRoute.of(context)?.animation;
    if (entry == _entry) return;
    _entry?.removeStatusListener(_onEntry);
    _entry = entry?..addStatusListener(_onEntry);
  }

  /// 推入动画（连同从对话页过来时键盘收起）途中尺寸会连着变几次，停稳后再按电脑上的画面重来一遍。
  /// 第一份快照还没到时不用：本地是空的，没什么可错位，正在路上的快照就是按现在的尺寸订的，
  /// 重订会把它丢掉，空白多等一个来回。
  void _onEntry(AnimationStatus status) {
    if (status == AnimationStatus.completed && _painted) _schedule(fresh: true);
  }

  @override
  void dispose() {
    _resizeTimer?.cancel();
    _entry?.removeStatusListener(_onEntry);
    _noticeTimer?.cancel();
    _gen++;
    WidgetsBinding.instance.removeObserver(this);
    widget.model.removeListener(_onModel);
    unawaited(_detach());
    _controller.dispose();
    _deskScroll.dispose();
    _pose.dispose();
    _contentRows.dispose();
    _focus.dispose();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // 回到前台总是重取一次画面：后台期间连接可能卡过，留着的半截画面不会自己好
    if (state == AppLifecycleState.resumed) _schedule(fresh: true);
  }

  String _titleKey = '';
  AgentState? _agentState;

  void _onModel() {
    if (widget.model.rpc != _bound) _schedule();
    // 标题或状态变了才重建，轮询没变化时不动终端视图
    final tab = _liveTab;
    // agent 停下来（一轮做完、等人）时按电脑上的画面重来一遍：手机上偶尔会和电脑对不上，
    // agent 只重画变了的格子，错位的地方不会自己好；停下时重取，不打断它干活时的输出
    final agentState = agentStateOf(tab.state);
    if (_agentState == AgentState.working && agentState != AgentState.working) {
      _schedule(fresh: true);
    }
    _agentState = agentState;
    final key =
        '${tab.title}|${tab.state}|${tab.toolName}|${widget.model.settings.terminalFontSize}|$_ended|${widget.model.rpc == null}';
    if (key != _titleKey && mounted) setState(() => _titleKey = key);
  }

  /// 尺寸停止变化多久才重新订阅。
  static const _resizeSettle = Duration(milliseconds: 150);
  Timer? _resizeTimer;

  void _onResize(int cols, int rows) {
    // 电脑布局把控件排成桌面的格子，这里报上来的是电脑的列数。记成手机视口的话，
    // 关掉电脑布局会按桌面宽度去 HoldSize。
    if (_desk) return;
    // 推入页面、转屏、从后台回来重新布局时一次会连着报好几个尺寸。每个都订阅的话，电脑上的终端
    // 跟着连改几次尺寸，agent 每次都重画，手机也连着复位、写快照几次。等尺寸稳定下来只订阅一次；
    // 途中收到的旧尺寸输出会画乱本地画面，由这次的快照盖掉
    // 还没写过快照时本地是空的，裁不坏什么；尺寸和订阅时一样就不用重订
    if (_painted) _resizes.changed();
    _resizeTimer?.cancel();
    _resizeTimer = Timer(_resizeSettle, () {
      if (!mounted || _desk || !_resizes.report(cols, rows)) return;
      _cols = cols;
      _rows = rows;
      _schedule(fresh: true);
    });
  }

  /// 电脑布局时 flterm 的 onResize 不能用，手机这块区域的列数从布局算，留给关掉时订阅。
  void _notePhoneBox(BoxConstraints box) {
    if (!_desk && _cols >= 20) return;
    final cell = _cellMetrics(widget.model.settings.terminalFontSize);
    if (cell.cellWidth <= 0 || cell.cellHeight <= 0) return;
    var cols = (box.maxWidth / cell.cellWidth).floor();
    if (_cropGrok) cols += _grokMargin * 2;
    final rows = (box.maxHeight / cell.cellHeight).floor();
    if (cols < 20 || rows < 8) return;
    if (cols == _cols && rows == _rows) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (!_desk && _cols >= 20) return;
      if (cols == _cols && rows == _rows) return;
      _cols = cols;
      _rows = rows;
      _resizes.set(cols, rows);
      _schedule();
    });
  }

  /// 每次量都要排两段文字，电脑布局一次构建要量好几次，按字号记下来。
  final _metricsCache = <(double, double), CellMetrics>{};

  CellMetrics _cellMetrics(double fontSize) {
    final dpr = MediaQuery.devicePixelRatioOf(context);
    if (_metricsCache.length > 32) _metricsCache.clear();
    return _metricsCache.putIfAbsent(
      (fontSize, dpr),
      () => measureCellMetrics(
        fontSize: fontSize,
        fontFamily: monoFamily,
        fontFamilyFallback: monoFallback,
        devicePixelRatio: dpr,
      ),
    );
  }

  /// [fresh] 时尺寸没变也重新订阅，拿一份新快照。
  void _schedule({bool fresh = false}) {
    // 尺寸还在变（推入动画、从后台回来时重新布局）：等它停下来那次订阅，现在按旧尺寸订一份只会多一次错位
    if (fresh && (_resizeTimer?.isActive ?? false)) return;
    _gate = _gate.then((_) => _open(fresh: fresh)).catchError((Object _) {});
  }

  Future<void> _open({bool fresh = false}) async {
    final rpc = widget.model.rpc;
    if (!mounted || rpc == null || _cols < 20 || _rows < 8) return;
    final desk = _desk;
    if (!fresh &&
        identical(rpc, _bound) &&
        _stream != null &&
        _heldDesk == desk &&
        (desk || (_cols == _heldCols && _rows == _heldRows))) {
      return;
    }
    final gen = ++_gen;
    final cols = _cols;
    final rows = _rows;
    // 先用新尺寸订阅，再退掉旧的。常驻进程的尺寸锁定是叠起来的：先退旧的会有一瞬间没有手机，
    // 终端退回桌面的宽度，agent 按桌面宽度重画一遍，屏幕就乱了。
    final oldRpc = _bound;
    final oldStream = _stream;
    final oldBinary = _binary;
    _asm.expect();
    _bullets = BulletFix();
    _binary = rpc.binary.listen(
      (bytes) {
        if (!mounted || gen != _gen) return;
        final piece = _asm.add(bytes);
        if (piece != null) _applyPiece(piece);
      },
      onDone: () {
        if (gen != _gen) return;
        _stream = null;
        _bound = null;
      },
    );
    await oldBinary?.cancel();
    try {
      // 电脑布局不带手机尺寸：常驻进程不改终端尺寸，电脑上照常
      final stream = await rpc.subscribe('terminal.subscribe', {
        'terminal': widget.tab.terminalId,
        if (!desk) 'viewport': {'cols': cols, 'rows': rows},
      });
      if (gen != _gen || !mounted) {
        await _drop(rpc, stream);
        return;
      }
      _bound = rpc;
      _stream = stream;
      _heldCols = cols;
      _heldRows = rows;
      _heldDesk = desk;
      stream.frames.listen((frame) {
        if (!mounted || gen != _gen) return;
        final sid = frame['streamId'];
        if (frame['type'] == 'subscribed' && sid is num) {
          for (final piece in _asm.accept(sid.toInt())) {
            _applyPiece(piece);
          }
        }
        if (frame['type'] == 'end') {
          setState(() => _notice = context.l10n.terminalExited);
        }
        // 断线由重连接手，这里不提示。
      }, onError: (Object _) {});
    } catch (error) {
      if (gen != _gen || !mounted) return;
      await _binary?.cancel();
      _binary = null;
      setState(() => _notice = friendlyError(error, context.l10n));
    } finally {
      if (oldRpc != null && oldStream != null) {
        await _drop(oldRpc, oldStream);
      }
    }
  }

  Future<void> _detach() async {
    final rpc = _bound;
    final stream = _stream;
    _stream = null;
    _bound = null;
    _heldCols = 0;
    _heldRows = 0;
    await _binary?.cancel();
    _binary = null;
    if (rpc != null && stream != null) await _drop(rpc, stream);
  }

  Future<void> _drop(PhoneRpc rpc, RpcStream stream) async {
    unawaited(
      rpc
          .call('terminal.unsubscribe', {'requestId': stream.id})
          .catchError((Object _) => <String, Object?>{}),
    );
    await stream.cancel();
  }

  /// 快照和输出是远端画面。写进去时本地模拟器可能应答查询，那些字节不能再送回 PTY。
  Widget _terminalView(
    PosseColors c, {
    double? fontSize,
    ScrollPhysics? scrollPhysics,
    TerminalScrollController? scrollController,
    EdgeInsets backgroundBleed = EdgeInsets.zero,
  }) => TerminalView(
    key: _viewKey,
    controller: _controller,
    theme: terminalTheme(
      c,
      fontSize: fontSize ?? widget.model.settings.terminalFontSize,
    ),
    // 进来先看画面，点一下终端才弹键盘，不然键盘挡住半屏
    autofocus: false,
    focusNode: _focus,
    padding: EdgeInsets.zero,
    backgroundBleed: backgroundBleed,
    scrollPhysics: scrollPhysics,
    scrollController: scrollController,
    linkSettings: LinkSettings(onActivate: _onLink),
    onContentRows: _onContentRows,
    onTap: _showKeyboard,
  );

  void _onContentRows(int rows) => _contentRows.value = rows;

  /// 轻点终端里的链接：网址交给系统浏览器，工作区里的文件在文件页里看。
  void _onLink(ActivatedLink link) {
    final root = widget.model.worktrees
        .where((w) => w.id == widget.tab.worktreeId)
        .firstOrNull
        ?.path;
    final target = resolveLink(
      uri: link.uri,
      filePath: link.file?.path,
      resolvedPath: link.file?.resolvedPath,
      root: root,
    );
    switch (target) {
      case OpenUrl(:final uri):
        unawaited(_openUrl(uri));
      case OpenFile(:final path):
        Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => FileViewer(
              model: widget.model,
              worktree: widget.tab.worktreeId,
              path: path,
            ),
          ),
        );
      case NotInWorkspace():
        _flash(context.l10n.terminalNotInWorkspace);
      case null:
        break;
    }
  }

  Future<void> _openUrl(Uri uri) async {
    var ok = false;
    try {
      ok = await launchUrl(uri, mode: LaunchMode.externalApplication);
    } catch (_) {}
    if (!ok && mounted) _flash(context.l10n.terminalCantOpenLink('$uri'));
  }

  /// grok 自己在左右各留 [_grokMargin] 列空白、整屏铺底色，窄屏上再加外面的内边距显得空。
  /// 手机布局时把终端排宽 2×[_grokMargin] 列，左右各裁掉这么多，grok 的内容贴着屏幕边。
  bool get _cropGrok => widget.tab.agent == 'grok' && !_desk;

  Widget _croppedView(PosseColors c, BoxConstraints box) {
    final cell = _cellMetrics(widget.model.settings.terminalFontSize);
    final margin = cell.cellWidth * _grokMargin;
    final width = box.maxWidth + margin * 2;
    return ClipRect(
      child: Transform.translate(
        offset: Offset(-margin, 0),
        child: OverflowBox(
          alignment: Alignment.topLeft,
          minWidth: width,
          maxWidth: width,
          child: _terminalView(c),
        ),
      ),
    );
  }

  /// 电脑布局：按快照的列和行排成桌面那一块，再缩进屏幕宽度。手势在这块表面上，
  /// 不另加横向滚动，免得和终端自己的纵向滚动抢。还没有快照时先铺满。
  Widget _deskView(PosseColors c, BoxConstraints box) {
    // 程序自己铺了底色（比如 grok）时，外面的内边距接着画它的底色，不露出一圈主题色；
    // 电脑布局能缩放、平移，不往外延
    if (!_desk || _deskCols <= 0) {
      if (_cropGrok) return _croppedView(c, box);
      return _terminalView(c, backgroundBleed: _termPadding);
    }
    final font = _deskFont(box.maxWidth);
    final cell = _cellMetrics(font);
    final gridW = (_deskCols + 0.5) * cell.cellWidth;
    final gridH = _deskRows > 0
        ? (_deskRows + 0.5) * cell.cellHeight
        : box.maxHeight;
    return SizedBox.expand(
      key: _deskKey,
      child: ClipRect(
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          // 点在格子外面的空白处：格子里的点击由 flterm 先认领，到不了这里
          onTap: _showKeyboard,
          onScaleStart: _scaleStart,
          onScaleUpdate: (d) => _scaleUpdate(d, gridW, gridH, box),
          onScaleEnd: (d) => _scaleEnd(gridW, gridH, box),
          // 外面是屏幕大小的紧约束，格子要按桌面的列数排，不然 flterm 算出的列数变少、行尾折下去。
          // 平移放在 OverflowBox 里面：它按屏幕大小判断点击，里面的 Transform 再换算到格子上。
          child: OverflowBox(
            alignment: Alignment.topLeft,
            minWidth: 0,
            maxWidth: double.infinity,
            minHeight: 0,
            maxHeight: double.infinity,
            child: ValueListenableBuilder<int>(
              valueListenable: _pose,
              builder: (context, _, child) => Transform(
                alignment: Alignment.topLeft,
                transform: Matrix4.identity()
                  ..translateByDouble(_panX, _panY, 0, 1)
                  ..scaleByDouble(_pinch, _pinch, 1, 1),
                child: child,
              ),
              child: SizedBox(
                width: gridW,
                height: gridH,
                child: _terminalView(
                  c,
                  fontSize: font,
                  scrollPhysics: const NeverScrollableScrollPhysics(),
                  scrollController: _deskScroll,
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  /// 电脑布局静止时的字号：缩进屏幕宽度，再乘吸定的倍数。
  double _deskFont(double width) {
    final base = widget.model.settings.terminalFontSize;
    final zoom = widget.model.settings.deskZoom;
    final baseCell = _cellMetrics(base);
    // 多留半格，免得按像素取整后少算一列
    final fit = deskFitScale(
      viewportWidth: width,
      gridWidth: (_deskCols + 0.5) * baseCell.cellWidth,
    );
    return (base * fit * zoom).clamp(2.0, 64.0);
  }

  /// 终端区域顶端到最后一行内容底边的距离。
  double _contentBottom(double width) {
    final rows = _contentRows.value;
    if (_cropGrok) {
      return rows *
          _cellMetrics(widget.model.settings.terminalFontSize).cellHeight;
    }
    width -= _termPadding.horizontal;
    if (!_desk || _deskCols <= 0) {
      return _termPadding.top +
          rows *
              _cellMetrics(widget.model.settings.terminalFontSize).cellHeight;
    }
    // 不算平移和捏合：键盘弹着时上下拖动照常平移，不被挪动抵消
    final cell = _cellMetrics(_deskFont(width));
    return _termPadding.top + rows * cell.cellHeight;
  }

  RenderBox? get _deskBox {
    final box = _deskKey.currentContext?.findRenderObject();
    if (box is RenderBox && box.hasSize) return box;
    return null;
  }

  void _scaleStart(ScaleStartDetails _) {
    _pinching = false;
  }

  void _scaleUpdate(
    ScaleUpdateDetails details,
    double gridW,
    double gridH,
    BoxConstraints box,
  ) {
    // Posse: 鼠标跟踪时这一指也不交给程序拖选，滑动仍平移和滚动。
    final render = _deskBox;
    if (render == null) return;
    final local = render.globalToLocal(details.focalPoint);
    if (details.pointerCount < 2 && _controller.hasSelection) return;
    if (details.pointerCount >= 2) {
      if (!_pinching) {
        // 第二指落下时才记下锚点，单指平移不占用这个点
        _pinching = true;
        _controller.clearSelection();
        _pinchScale0 = details.scale == 0 ? 1 : details.scale;
        _focalContent = Offset(
          (local.dx - _panX) / _pinch,
          (local.dy - _panY) / _pinch,
        );
      }
      final zoom = widget.model.settings.deskZoom;
      final raw = details.scale / _pinchScale0;
      final lo = deskZoomStops.first / zoom;
      final hi = deskZoomStops.last / zoom;
      final next = raw.clamp(lo, hi);
      _pinch = next;
      _panX = local.dx - _focalContent.dx * next;
      _panY = local.dy - _focalContent.dy * next;
      final clamped = clampDeskPan(
        panX: _panX,
        panY: _panY,
        contentWidth: gridW * next,
        contentHeight: gridH * next,
        viewportWidth: box.maxWidth,
        viewportHeight: box.maxHeight,
      );
      _panX = clamped.x;
      _panY = clamped.y;
      _pose.value++;
      return;
    }
    if (_pinching) {
      _commitPinch(gridW, gridH, box);
      return;
    }
    final drag = deskDrag(
      panX: _panX,
      panY: _panY,
      dx: details.focalPointDelta.dx,
      dy: details.focalPointDelta.dy,
      contentWidth: gridW * _pinch,
      contentHeight: gridH * _pinch,
      viewportWidth: box.maxWidth,
      viewportHeight: box.maxHeight,
    );
    _panX = drag.panX;
    _panY = drag.panY;
    _scrollDesk(drag.scrollDy);
    _pose.value++;
  }

  void _scaleEnd(double gridW, double gridH, BoxConstraints box) {
    if (_pinching) _commitPinch(gridW, gridH, box);
  }

  /// 松手吸到固定档，按新倍数重排字号。锚点按捏合开始时按住的那一格换算。
  void _commitPinch(double gridW, double gridH, BoxConstraints box) {
    final zoom = widget.model.settings.deskZoom;
    final pinch = _pinch;
    final next = snapDeskZoom(zoom * pinch);
    final k = zoom == 0 ? 1.0 : next / zoom;
    _pinch = 1;
    _pinching = false;
    _panX += _focalContent.dx * (pinch - k);
    _panY += _focalContent.dy * (pinch - k);
    final clamped = clampDeskPan(
      panX: _panX,
      panY: _panY,
      contentWidth: gridW * k,
      contentHeight: gridH * k,
      viewportWidth: box.maxWidth,
      viewportHeight: box.maxHeight,
    );
    _panX = clamped.x;
    _panY = clamped.y;
    if (next != zoom) {
      widget.model.updateSettings((s) => s.deskZoom = next);
    }
    if (mounted) setState(() {});
  }

  void _scrollDesk(double dy) {
    if (dy == 0 || !_deskScroll.hasClients) return;
    final pos = _deskScroll.position;
    if (!pos.minScrollExtent.isFinite || !pos.maxScrollExtent.isFinite) {
      // 备用屏幕没有回滚范围。改像素后由终端换成滚轮或方向键。
      pos.jumpTo(pos.pixels - dy);
      return;
    }
    final next = (pos.pixels - dy).clamp(
      pos.minScrollExtent,
      pos.maxScrollExtent,
    );
    if (next != pos.pixels) pos.jumpTo(next);
  }

  /// 快照带着终端的列数和行数：电脑布局按它排，电脑那边改了窗口大小会重发快照。
  void _applyPiece(TermBytes piece) {
    if (piece.snapshot &&
        piece.cols > 0 &&
        (piece.cols != _deskCols || piece.rows != _deskRows)) {
      setState(() {
        _deskCols = piece.cols;
        _deskRows = piece.rows;
        _panX = 0;
        _panY = 0;
        _pinch = 1;
      });
    }
    // 快照不清屏，是从左上角逐行盖上去的。重新订阅或电脑改了尺寸时本地终端里还有旧画面，
    // 先完全复位、清掉回滚，不然没盖到的格子留成碎片，回滚区也会重复一份
    if (piece.snapshot) {
      _controller.clearSelection();
      _apply(_resetBytes);
    }
    _apply(piece.bytes);
    if (piece.snapshot && !_painted) setState(() => _painted = true);
  }

  /// 新订阅从快照开始，切开的半个字符不能接到上一次的输出上。
  var _bullets = BulletFix();

  void _apply(Uint8List bytes) {
    _applying = true;
    try {
      _controller.write(_bullets.apply(bytes));
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error, context.l10n));
    } finally {
      _applying = false;
    }
  }

  void _onOutput(Uint8List bytes) {
    if (_applying || bytes.isEmpty) return;
    unawaited(_send(bytes));
  }

  /// 快捷键栏的「发送文件」：相册里的照片或视频、拍照、「文件」App 里的，传到电脑上后把路径打进终端。
  Future<void> _sendFile() async {
    final rpc = widget.model.rpc;
    if (rpc == null || _busy) return;
    // 选文件的面板和相册都不需要键盘，挡着反而看不见
    _focus.unfocus();
    setState(() => _busy = true);
    try {
      final ask = await askFileSource(context);
      if (ask == null || !mounted) return;
      final file = await pickAskedFile(ask);
      if (file == null || !mounted) return;
      setState(() {
        _upload = 0;
        _uploadFile = true;
      });
      final String path;
      try {
        path = await uploadFile(
          rpc,
          file,
          onProgress: (done) {
            if (mounted) setState(() => _upload = done);
          },
        );
      } finally {
        if (mounted) setState(() => _upload = null);
      }
      if (!mounted) return;
      // 和图片一样当成一次粘贴、不回车；路径里的空格这些转义掉，shell 和 agent 都当成一个文件
      _controller.paste(shellPath(path));
    } catch (error) {
      if (mounted) _flash(friendlyError(error, context.l10n));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// 文字交给终端自己的粘贴（不回车）。图片上传后只把路径打进去，和发送图片一样。
  Future<void> _pasteClipboard() async {
    final rpc = widget.model.rpc;
    if (rpc == null || _busy) return;
    setState(() => _busy = true);
    try {
      final paste = await readClipboardPaste();
      if (!mounted) return;
      if (paste.isText) {
        _controller.paste(paste.text!);
        return;
      }
      if (paste.isImage) {
        await _typeImagePath(rpc, paste.image!);
        return;
      }
      _flash(context.l10n.terminalClipboardEmpty);
    } catch (error) {
      if (mounted) _flash(friendlyError(error, context.l10n));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _typeImagePath(PhoneRpc rpc, Uint8List bytes) async {
    setState(() {
      _upload = 0;
      _uploadFile = false;
    });
    final String path;
    try {
      path = await uploadImage(
        rpc,
        bytes,
        onProgress: (done) {
          if (mounted) setState(() => _upload = done);
        },
      );
    } finally {
      if (mounted) setState(() => _upload = null);
    }
    if (!mounted) return;
    // 和桌面端一样当成一次粘贴发（程序开了括号粘贴就包上）：Claude Code、Codex 认出粘贴的是图片文件，
    // 显示成 [Image #1] 当附件，而不是一长串路径。不回车：还能接着写说明，shell 里也不会把路径当命令执行
    _controller.paste(path);
  }

  /// 专用键和自定义键按程序当前的光标键模式编码，见 [cursorKeysApp]。
  Future<void> _sendKey(List<int> bytes, {bool enter = false}) => _send(
    _controller.modeGet(const TerminalMode.cursorKeys())
        ? cursorKeysApp(bytes)
        : bytes,
    enter: enter,
  );

  /// 快捷键栏的「清空」：agent 的输入框可能有好几行，分两次发 [clearLines]，最多清 40 行；
  /// shell 只删当前行，在空行上退格会响铃。
  Future<void> _clearInput() async {
    final agent = _liveTab.agent;
    if (agent == null) return _send(const [0x05, 0x15]);
    // 一个键一个键地送，20 行就要 1 秒多，只送一次
    if (sendsKeysOneByOne(agent)) {
      return _send(
        [],
        keys: [
          for (final b in clearLines()) [b],
        ],
      );
    }
    await _send(clearLines());
    await _send(clearLines());
  }

  /// 快捷键栏的「Exit」：清空输入框，写上 /exit，不回车，由用户自己发送（agent 正在干活时不会被误退）。
  /// Claude Code、Codex、Grok、Cursor 都认这条命令；不发 Ctrl+D：agent 退出后多出来的一次会落到 shell 上，
  /// 把整个终端也关掉。
  Future<void> _exitAgent() async {
    if (_liveTab.agent == null) {
      _flash(context.l10n.terminalNoAgent);
      return;
    }
    if (widget.model.rpc == null) return;
    await _clearInput();
    // 和清空的按键挨得太近时，Claude Code 把它们当成同一次粘贴
    await Future<void>.delayed(const Duration(milliseconds: 300));
    if (mounted) await _send(utf8.encode('/exit'));
  }

  /// [keys] 不为空时代替 [bytes]，见 [sendTerminal]。
  Future<void> _send(
    List<int> bytes, {
    bool enter = false,
    List<List<int>>? keys,
  }) async {
    final rpc = widget.model.rpc;
    if (rpc == null || (bytes.isEmpty && keys == null)) return;
    final outcome = await sendTerminal(
      rpc,
      terminal: widget.tab.terminalId,
      raw: keys == null ? bytes : null,
      keys: keys,
      enter: enter,
    );
    final note = sendOutcomeText(outcome);
    if (!mounted || note.isEmpty) return;
    _flash(note);
  }

  /// 连着电脑、工作区列表也回来了，却找不到这个终端：它已经关掉了（或者常驻进程重启过）。
  bool get _ended {
    final model = widget.model;
    if (model.rpc == null || model.worktrees.isEmpty) return false;
    final tabs = model.tabs[widget.tab.worktreeId] ?? const <SessionTab>[];
    return !tabs.any((t) => t.terminalId == widget.tab.terminalId);
  }

  /// 首页每 3 秒刷新的那份标签里找这个终端，状态跟着变；找不到就用打开时的。
  SessionTab get _liveTab {
    for (final tab in widget.model.tabs[widget.tab.worktreeId] ?? const []) {
      if (tab.terminalId == widget.tab.terminalId) return tab;
    }
    return widget.tab;
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Scaffold(
      backgroundColor: c.card,
      appBar: AppBar(
        backgroundColor: c.band,
        titleSpacing: 0,
        title: SessionTitle(tab: _liveTab, model: widget.model, ended: _ended),
        actions: [
          FilesButton(
            model: widget.model,
            worktree: widget.tab.worktreeId,
            title: worktreePath(widget.model, widget.tab.worktreeId),
          ),
          // 切到对话放进"更多"里，顶栏只留文件和更多
          IconButton(
            tooltip: context.l10n.commonMore,
            icon: const Icon(Icons.more_horiz_rounded),
            onPressed: () async {
              final nav = Navigator.of(context);
              final action = await showSessionMore(
                context,
                tab: widget.tab,
                switchLabel: widget.onOpenChat == null
                    ? null
                    : context.l10n.terminalSwitchToChat,
                switchIcon: Icons.chat_bubble_outline_rounded,
                media: hasMedia(_liveTab),
                tools: hasTools(_liveTab),
                // 打开时终端保持电脑的尺寸，手机左右滑着看；关着时按手机屏幕重排
                desk: _desk,
                onDesk: (value) {
                  widget.model.updateSettings((s) => s.deskLayout = value);
                  if (!mounted) return;
                  setState(() {});
                  _schedule();
                },
              );
              if (!mounted) return;
              if (action == 'media') {
                nav.push(
                  MaterialPageRoute<void>(
                    builder: (_) =>
                        MediaScreen(model: widget.model, tab: _liveTab),
                  ),
                );
              }
              if (action == 'tools') {
                nav.push(
                  MaterialPageRoute<void>(
                    builder: (_) =>
                        ToolsScreen(model: widget.model, tab: _liveTab),
                  ),
                );
              }
              if (action == 'switch') widget.onOpenChat?.call();
              if (action == 'close' &&
                  await closeSession(this.context, widget.model, widget.tab)) {
                nav.pop();
              }
            },
          ),
          const SizedBox(width: 4),
        ],
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(1),
          child: Divider(height: 1, color: c.line),
        ),
      ),
      // 键盘弹出时终端不跟着变矮：行数不变，电脑上的 agent 不用按新尺寸重画。
      // 快捷键栏贴着键盘，终端整体往上挪到最后一行内容露出来，挪出顶端的部分裁掉
      resizeToAvoidBottomInset: false,
      body: LayoutBuilder(
        builder: (context, body) {
          final keyboard = MediaQuery.viewInsetsOf(context).bottom;
          final safe = MediaQuery.viewPaddingOf(context).bottom;
          final height = body.maxHeight - _keyBarHeight - safe;
          // 键盘盖住的终端高度：快捷键栏下面的安全区本来就不算终端
          final covered = (keyboard - safe).clamp(0.0, height);
          return Stack(
            children: [
              Positioned(
                top: 0,
                left: 0,
                right: 0,
                height: height,
                child: ClipRect(
                  child: ListenableBuilder(
                    listenable: _contentRows,
                    builder: (context, child) {
                      final lift = keyboardLift(
                        contentBottom: _contentBottom(body.maxWidth),
                        visible: height - covered,
                        covered: covered,
                      );
                      return Transform.translate(
                        offset: Offset(0, -lift),
                        child: child,
                      );
                    },
                    child: _terminalArea(c),
                  ),
                ),
              ),
              if (!_painted && _notice == null && !_ended)
                Positioned(
                  top: 0,
                  left: 0,
                  right: 0,
                  height: height,
                  child: const IgnorePointer(
                    child: Center(child: PosseLoader()),
                  ),
                ),
              // 提示和上传进度浮在看得见的那块终端上面，不跟着终端挪：
              // 占布局的话终端会变矮，agent 跟着重排。复制按钮贴着选区，跟着画面挪。
              if (_upload case final done?)
                Positioned(
                  left: 0,
                  right: 0,
                  bottom: body.maxHeight - height + covered + 12,
                  child: UploadBar(
                    index: 0,
                    count: 1,
                    done: done,
                    files: _uploadFile,
                  ),
                ),
              if (_notice != null)
                Positioned(
                  top: 12,
                  left: 4,
                  right: 4,
                  child: NoticeBar(
                    _notice!,
                    floating: true,
                    onClose: () => _flash(null),
                  ),
                ),
              Positioned.fill(
                child: ListenableBuilder(
                  listenable: _moving,
                  builder: (context, _) =>
                      _selectionChrome(body.maxWidth, height, covered),
                ),
              ),
              Positioned(
                left: 0,
                right: 0,
                bottom: keyboard,
                child: _KeyBar(
                  onKey: _sendKey,
                  onExit: () => unawaited(_exitAgent()),
                  onClear: () => unawaited(_clearInput()),
                  keys: widget.model.settings.barKeys,
                  onCustom: (key) => _sendKey(key.bytes, enter: key.enter),
                  onImage: widget.model.rpc == null || _busy ? null : _sendFile,
                  onPaste: widget.model.rpc == null || _busy
                      ? null
                      : _pasteClipboard,
                  keyboardUp: keyboard > 0,
                  onKeyboard: _toggleKeyboard,
                ),
              ),
            ],
          );
        },
      ),
    );
  }

  Widget _selectionChrome(double width, double height, double covered) {
    final cell = _cellMetrics(
      _desk && _deskCols > 0
          ? _deskFont(width - _termPadding.horizontal)
          : widget.model.settings.terminalFontSize,
    );
    return SelectionChrome(
      controller: _controller,
      width: width,
      height: height,
      covered: covered,
      cellWidth: cell.cellWidth,
      cellHeight: cell.cellHeight,
      padLeft: _cropGrok ? 0 : _termPadding.left,
      padTop: _cropGrok ? 0 : _termPadding.top,
      lift: keyboardLift(
        contentBottom: _contentBottom(width),
        visible: height - covered,
        covered: covered,
      ),
      pinch: _desk ? _pinch : 1,
      panX: _desk ? _panX : 0,
      panY: _desk ? _panY : 0,
      cropX: _cropGrok ? cell.cellWidth * _grokMargin : 0,
    );
  }

  Widget _terminalArea(PosseColors c) {
    return Padding(
      padding: _cropGrok ? EdgeInsets.zero : _termPadding,
      child: LayoutBuilder(
        builder: (context, constraints) {
          _notePhoneBox(constraints);
          return _deskView(c, constraints);
        },
      ),
    );
  }
}

/// 终端底色和卡片一致，前景用 Posse 的文字色，等宽字体用 Menlo；字色和底色至少差 3 倍对比度。
TerminalTheme terminalTheme(PosseColors c, {double fontSize = 12.5}) {
  final base = c.dark ? TerminalTheme.dark() : TerminalTheme.light();
  return TerminalTheme(
    palette: ColorPalette(
      ansiColors: base.palette.ansiColors,
      background: c.card,
      foreground: c.text,
    ),
    fontFamily: monoFamily,
    fontFamilyFallback: monoFallback,
    fontSize: fontSize,
    // 程序的配色跟着电脑的深浅色，手机换成另一种时暗灰、黄色这类字会糊进底色
    minimumContrast: 3,
  );
}

/// 终端四周留的空，文字不贴屏幕边。程序铺了底色时这一圈会接着画它的底色，看着是一片，
/// 加上程序自己的缩进（grok 左边 2 到 5 列）显得空，所以只留一点。
const _termPadding = EdgeInsets.fromLTRB(4, 4, 4, 0);

/// 快捷键栏不算底部安全区的高度：40 的按键行加 1 的上边线。
const _keyBarHeight = 41.0;

/// grok 左右各自留的空白列数（46 列宽时实测）。
const _grokMargin = 2;

class _KeyBar extends StatelessWidget {
  const _KeyBar({
    required this.onKey,
    required this.keys,
    required this.onCustom,
    required this.onImage,
    required this.onPaste,
    required this.keyboardUp,
    required this.onKeyboard,
    required this.onExit,
    required this.onClear,
  });

  final Future<void> Function(List<int> bytes) onKey;
  final VoidCallback onExit;
  final VoidCallback onClear;

  /// 键盘开关后面的键，按设置里排的顺序。
  final List<BarKey> keys;
  final void Function(CustomKey key) onCustom;
  final VoidCallback? onImage;
  final VoidCallback? onPaste;
  final bool keyboardUp;
  final VoidCallback onKeyboard;

  Widget _iconKey(
    PosseColors c, {
    IconData? icon,
    Widget? glyph,
    double size = 18,
    required String label,
    required VoidCallback? onTap,
  }) {
    return Semantics(
      button: true,
      label: label,
      child: Material(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.button),
        child: InkWell(
          borderRadius: BorderRadius.circular(Rd.button),
          onTap: onTap,
          child: Container(
            constraints: const BoxConstraints(minWidth: 40),
            padding: glyph == null
                ? null
                : const EdgeInsets.symmetric(horizontal: 8),
            alignment: Alignment.center,
            child: glyph ?? Icon(icon, size: size, color: c.text),
          ),
        ),
      ),
    );
  }

  Widget _textKey(PosseColors c, String label, VoidCallback onTap) {
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onTap,
        child: Container(
          constraints: const BoxConstraints(minWidth: 40),
          padding: const EdgeInsets.symmetric(horizontal: 10),
          alignment: Alignment.center,
          child: Text(
            label,
            maxLines: 1,
            style: TextStyle(
              fontSize: Fs.small,
              fontWeight: Fw.medium,
              color: c.text,
            ),
          ),
        ),
      ),
    );
  }

  /// 键盘的图形扁（24 格里只占 14 高），和图片同为 18 时看着偏小，所以单独放大；
  /// 粘贴的图形本来就比图片高，只放大一点。
  static const _keyboardIcon = 21.0;
  static const _pasteIcon = 19.0;

  /// 键盘开关：弹着时放下来看输出，收着时点它弹出来打字。
  Widget _hideKeyboard(PosseColors c) {
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onKeyboard,
        child: SizedBox(
          width: 40,
          child: Icon(
            keyboardUp ? Icons.keyboard_hide_rounded : Icons.keyboard_rounded,
            size: _keyboardIcon,
            color: c.text,
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Container(
      decoration: BoxDecoration(
        color: c.band,
        border: Border(top: BorderSide(color: c.line)),
      ),
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: _keyBarHeight - 1,
          child: ListView.separated(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
            itemCount: keys.length + 1,
            separatorBuilder: (_, _) => const SizedBox(width: 6),
            itemBuilder: (context, i) {
              if (i == 0) return _hideKeyboard(c);
              final key = keys[i - 1];
              final custom = key.custom;
              if (custom != null) {
                return _textKey(c, custom.shown, () => onCustom(custom));
              }
              final term = key.termKey;
              if (term != null && term.id == sendNowLabel) {
                return _iconKey(
                  c,
                  glyph: SendNowGlyph(color: c.text),
                  label: context.l10n.terminalSendNow,
                  onTap: () => onKey(term.bytes),
                );
              }
              if (term != null && term.id == exitAgentLabel) {
                return _textKey(c, term.label(context.l10n), onExit);
              }
              if (term != null && term.id == clearInputKey) {
                return _textKey(c, term.label(context.l10n), onClear);
              }
              if (term != null) {
                return _textKey(
                  c,
                  term.label(context.l10n),
                  () => onKey(term.bytes),
                );
              }
              return key.builtin == 'image'
                  ? _iconKey(
                      c,
                      icon: Icons.attach_file_rounded,
                      label: context.l10n.terminalSendFile,
                      onTap: onImage,
                    )
                  : _iconKey(
                      c,
                      icon: Icons.content_paste_rounded,
                      size: _pasteIcon,
                      label: context.l10n.commonPaste,
                      onTap: onPaste,
                    );
            },
          ),
        ),
      ),
    );
  }
}
