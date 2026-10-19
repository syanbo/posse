import 'dart:async';

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
import '../workspace/session_actions.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import 'desk_fit.dart';
import 'frames.dart';
import 'link_target.dart';

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

  final HiveAppModel model;
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
  var _heldCols = 0;
  var _heldRows = 0;

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

  /// 平移和捏合中只重画这一层，不重建整个终端页。
  final _pose = ValueNotifier<int>(0);
  var _pinching = false;
  var _pinchScale0 = 1.0;
  Offset _focalContent = Offset.zero;
  var _applying = false;
  var _gen = 0;
  Future<void> _gate = Future<void>.value();
  String? _notice;
  final _focus = FocusNode();
  var _busy = false;
  var _showCopy = false;

  void _onController() {
    final selected = _controller.hasSelection;
    if (selected == _showCopy || !mounted) return;
    setState(() => _showCopy = selected);
  }

  Future<void> _copySelection() async {
    final text = _controller.selectedText();
    _controller.clearSelection();
    if (text.isEmpty) return;
    await Clipboard.setData(ClipboardData(text: text));
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _controller = TerminalController()
      ..onOutput = _onOutput
      ..onResize = _onResize
      ..addListener(_onController);
    widget.model.addListener(_onModel);
  }

  @override
  void dispose() {
    _gen++;
    WidgetsBinding.instance.removeObserver(this);
    widget.model.removeListener(_onModel);
    unawaited(_detach());
    _controller.removeListener(_onController);
    _controller.dispose();
    _deskScroll.dispose();
    _pose.dispose();
    _focus.dispose();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && widget.model.rpc != _bound) {
      _schedule();
    }
  }

  String _titleKey = '';

  void _onModel() {
    if (widget.model.rpc != _bound) _schedule();
    // 标题或状态变了才重建，轮询没变化时不动终端视图
    final tab = _liveTab;
    final key =
        '${tab.title}|${tab.state}|${tab.toolName}|${widget.model.settings.terminalFontSize}|$_ended|${widget.model.rpc == null}';
    if (key != _titleKey && mounted) setState(() => _titleKey = key);
  }

  void _onResize(int cols, int rows) {
    // 电脑布局把控件排成桌面的格子，这里报上来的是电脑的列数。记成手机视口的话，
    // 关掉电脑布局会按桌面宽度去 HoldSize。
    if (_desk) return;
    if (cols < 20 || rows < 8) return;
    if (cols == _cols && rows == _rows) return;
    _cols = cols;
    _rows = rows;
    _schedule();
  }

  /// 电脑布局时 flterm 的 onResize 不能用，手机这块区域的列数从布局算，留给关掉时订阅。
  void _notePhoneBox(BoxConstraints box) {
    if (!_desk && _cols >= 20) return;
    final cell = _cellMetrics(widget.model.settings.terminalFontSize);
    if (cell.cellWidth <= 0 || cell.cellHeight <= 0) return;
    final cols = (box.maxWidth / cell.cellWidth).floor();
    final rows = (box.maxHeight / cell.cellHeight).floor();
    if (cols < 20 || rows < 8) return;
    if (cols == _cols && rows == _rows) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (!_desk && _cols >= 20) return;
      if (cols == _cols && rows == _rows) return;
      _cols = cols;
      _rows = rows;
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

  void _schedule() {
    _gate = _gate.then((_) => _open()).catchError((Object _) {});
  }

  Future<void> _open() async {
    final rpc = widget.model.rpc;
    if (!mounted || rpc == null || _cols < 20 || _rows < 8) return;
    final desk = _desk;
    if (identical(rpc, _bound) &&
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
          setState(() => _notice = '终端已经退出');
        }
      });
    } catch (error) {
      if (gen != _gen || !mounted) return;
      await _binary?.cancel();
      _binary = null;
      setState(() => _notice = '$error');
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
    HiveColors c, {
    double? fontSize,
    ScrollPhysics? scrollPhysics,
    TerminalScrollController? scrollController,
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
    scrollPhysics: scrollPhysics,
    scrollController: scrollController,
    linkSettings: LinkSettings(onActivate: _onLink),
  );

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
        setState(() => _notice = '这个文件不在当前工作区里，手机上打不开');
      case null:
        break;
    }
  }

  Future<void> _openUrl(Uri uri) async {
    var ok = false;
    try {
      ok = await launchUrl(uri, mode: LaunchMode.externalApplication);
    } catch (_) {}
    if (!ok && mounted) setState(() => _notice = '打不开这个链接：$uri');
  }

  /// 电脑布局：按快照的列和行排成桌面那一块，再缩进屏幕宽度。手势在这块表面上，
  /// 不另加横向滚动，免得和终端自己的纵向滚动抢。还没有快照时先铺满。
  Widget _deskView(HiveColors c, BoxConstraints box) {
    if (!_desk || _deskCols <= 0) return _terminalView(c);
    final base = widget.model.settings.terminalFontSize;
    final zoom = widget.model.settings.deskZoom;
    final baseCell = _cellMetrics(base);
    // 多留半格，免得按像素取整后少算一列
    final fit = deskFitScale(
      viewportWidth: box.maxWidth,
      gridWidth: (_deskCols + 0.5) * baseCell.cellWidth,
    );
    final font = (base * fit * zoom).clamp(2.0, 64.0);
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
    // Hive: 鼠标跟踪时这一指也不交给程序拖选，滑动仍平移和滚动。
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
  }

  /// 新订阅从快照开始，切开的半个字符不能接到上一次的输出上。
  var _bullets = BulletFix();

  void _apply(Uint8List bytes) {
    _applying = true;
    try {
      _controller.write(_bullets.apply(bytes));
    } catch (error) {
      if (mounted) setState(() => _notice = '$error');
    } finally {
      _applying = false;
    }
  }

  void _onOutput(Uint8List bytes) {
    if (_applying || bytes.isEmpty) return;
    unawaited(_send(bytes));
  }

  Future<void> _sendImage() async {
    final rpc = widget.model.rpc;
    if (rpc == null || _busy) return;
    setState(() => _busy = true);
    try {
      final ask = await askImageSource(context);
      if (ask == null || !mounted) return;
      final bytes = await pickAskedImage(ask);
      if (bytes == null || !mounted) return;
      await _typeImagePath(rpc, bytes);
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error));
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
      setState(() => _notice = '剪贴板里没有文字或图片');
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _typeImagePath(PhoneRpc rpc, Uint8List bytes) async {
    setState(() => _notice = '正在发送图片…');
    final path = await uploadImage(rpc, bytes);
    final outcome = await sendTerminal(
      rpc,
      terminal: widget.tab.terminalId,
      text: path,
      // 不回车：agent 里还能接着写说明，shell 里也不会把路径当命令执行
      enter: false,
    );
    if (!mounted) return;
    final note = sendOutcomeText(outcome);
    setState(() => _notice = note.isEmpty ? null : note);
  }

  Future<void> _send(List<int> bytes) async {
    final rpc = widget.model.rpc;
    if (rpc == null || bytes.isEmpty) return;
    final outcome = await sendTerminal(
      rpc,
      terminal: widget.tab.terminalId,
      raw: bytes,
      enter: false,
    );
    final note = sendOutcomeText(outcome);
    if (!mounted || note.isEmpty) return;
    setState(() => _notice = note);
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
    final c = HiveColors.of(context);
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
            tooltip: '更多',
            icon: const Icon(Icons.more_horiz_rounded),
            onPressed: () async {
              final nav = Navigator.of(context);
              final action = await showSessionMore(
                context,
                tab: widget.tab,
                switchLabel: widget.onOpenChat == null ? null : '切换到对话',
                switchIcon: Icons.chat_bubble_outline_rounded,
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
      body: Column(
        children: [
          ConnectionBanner(model: widget.model),
          if (_notice != null) NoticeBar(_notice!),
          Expanded(
            child: Padding(
              padding: const EdgeInsets.fromLTRB(8, 6, 4, 0),
              child: LayoutBuilder(
                builder: (context, constraints) {
                  _notePhoneBox(constraints);
                  return Stack(
                    fit: StackFit.expand,
                    children: [
                      _deskView(c, constraints),
                      if (_showCopy)
                        Positioned(
                          top: 8,
                          left: 0,
                          right: 0,
                          child: Row(
                            mainAxisAlignment: MainAxisAlignment.center,
                            children: [
                              _CopyChip(
                                onCopy: () => unawaited(_copySelection()),
                              ),
                            ],
                          ),
                        ),
                    ],
                  );
                },
              ),
            ),
          ),
          _KeyBar(
            onKey: _send,
            onImage: widget.model.rpc == null || _busy ? null : _sendImage,
            onPaste: widget.model.rpc == null || _busy ? null : _pasteClipboard,
            keyboardUp: MediaQuery.viewInsetsOf(context).bottom > 0,
            onKeyboard: () {
              if (MediaQuery.viewInsetsOf(context).bottom > 0) {
                _focus.unfocus();
              } else {
                _focus.requestFocus();
                // 已经有焦点但键盘被收起时，requestFocus 不会再弹，要让输入法重新出来
                SystemChannels.textInput.invokeMethod<void>('TextInput.show');
              }
            },
          ),
        ],
      ),
    );
  }
}

/// 长按选出的字留在这里。点一下写入剪贴板，点终端别处会先清掉选区。
class _CopyChip extends StatelessWidget {
  const _CopyChip({required this.onCopy});

  final VoidCallback onCopy;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Material(
      color: c.raised,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        onTap: onCopy,
        borderRadius: BorderRadius.circular(Rd.button),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 7),
          child: Text(
            '复制',
            style: TextStyle(
              color: c.accent,
              fontSize: Fs.meta,
              fontWeight: FontWeight.w500,
            ),
          ),
        ),
      ),
    );
  }
}

/// 终端底色和卡片一致，前景用 Hive 的文字色，等宽字体用 Menlo。
TerminalTheme terminalTheme(HiveColors c, {double fontSize = 12.5}) {
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
  );
}

class _KeyBar extends StatelessWidget {
  const _KeyBar({
    required this.onKey,
    required this.onImage,
    required this.onPaste,
    required this.keyboardUp,
    required this.onKeyboard,
  });

  final Future<void> Function(List<int> bytes) onKey;
  final VoidCallback? onImage;
  final VoidCallback? onPaste;
  final bool keyboardUp;
  final VoidCallback onKeyboard;

  Widget _iconKey(
    HiveColors c, {
    required IconData icon,
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
          child: SizedBox(
            width: 44,
            child: Icon(icon, size: 20, color: c.text),
          ),
        ),
      ),
    );
  }

  /// 键盘开关：弹着时放下来看输出，收着时点它弹出来打字。
  Widget _hideKeyboard(HiveColors c) {
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onKeyboard,
        child: SizedBox(
          width: 44,
          child: Icon(
            keyboardUp ? Icons.keyboard_hide_outlined : Icons.keyboard_outlined,
            size: 20,
            color: c.text,
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Container(
      decoration: BoxDecoration(
        color: c.band,
        border: Border(top: BorderSide(color: c.line)),
      ),
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: 48,
          child: ListView.separated(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
            itemCount: termKeys.length + 3,
            separatorBuilder: (_, _) => const SizedBox(width: 6),
            itemBuilder: (context, i) {
              if (i == 0) return _hideKeyboard(c);
              if (i == 1) {
                return _iconKey(
                  c,
                  icon: Icons.image_outlined,
                  label: '发送图片',
                  onTap: onImage,
                );
              }
              if (i == 2) {
                return _iconKey(
                  c,
                  icon: Icons.content_paste_rounded,
                  label: '粘贴',
                  onTap: onPaste,
                );
              }
              final key = termKeys[i - 3];
              return Material(
                color: c.fill,
                borderRadius: BorderRadius.circular(Rd.button),
                child: InkWell(
                  borderRadius: BorderRadius.circular(Rd.button),
                  onTap: () => onKey(key.bytes),
                  child: Container(
                    constraints: const BoxConstraints(minWidth: 44),
                    padding: const EdgeInsets.symmetric(horizontal: 12),
                    alignment: Alignment.center,
                    child: Text(
                      key.label,
                      style: TextStyle(
                        fontSize: 14,
                        fontWeight: FontWeight.w500,
                        color: c.text,
                      ),
                    ),
                  ),
                ),
              );
            },
          ),
        ),
      ),
    );
  }
}
