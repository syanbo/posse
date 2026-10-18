import 'dart:async';

import 'package:flterm/flterm.dart' hide TerminalScreen;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

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
import 'frames.dart';

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
  var _applying = false;
  var _gen = 0;
  Future<void> _gate = Future<void>.value();
  String? _notice;
  final _focus = FocusNode();
  var _busy = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _controller = TerminalController()
      ..onOutput = _onOutput
      ..onResize = _onResize;
    widget.model.addListener(_onModel);
  }

  @override
  void dispose() {
    _gen++;
    WidgetsBinding.instance.removeObserver(this);
    widget.model.removeListener(_onModel);
    unawaited(_detach());
    _controller.dispose();
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
    if (cols < 20 || rows < 8) return;
    if (cols == _cols && rows == _rows) return;
    _cols = cols;
    _rows = rows;
    _schedule();
  }

  void _schedule() {
    _gate = _gate.then((_) => _open()).catchError((Object _) {});
  }

  Future<void> _open() async {
    final rpc = widget.model.rpc;
    if (!mounted || rpc == null || _cols < 20 || _rows < 8) return;
    if (identical(rpc, _bound) &&
        _stream != null &&
        _cols == _heldCols &&
        _rows == _heldRows) {
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
    _binary = rpc.binary.listen(
      (bytes) {
        if (!mounted || gen != _gen) return;
        final piece = _asm.add(bytes);
        if (piece != null) _apply(piece.bytes);
      },
      onDone: () {
        if (gen != _gen) return;
        _stream = null;
        _bound = null;
      },
    );
    await oldBinary?.cancel();
    try {
      final stream = await rpc.subscribe('terminal.subscribe', {
        'terminal': widget.tab.terminalId,
        'viewport': {'cols': cols, 'rows': rows},
      });
      if (gen != _gen || !mounted) {
        await _drop(rpc, stream);
        return;
      }
      _bound = rpc;
      _stream = stream;
      _heldCols = cols;
      _heldRows = rows;
      stream.frames.listen((frame) {
        if (!mounted || gen != _gen) return;
        final sid = frame['streamId'];
        if (frame['type'] == 'subscribed' && sid is num) {
          for (final piece in _asm.accept(sid.toInt())) {
            _apply(piece.bytes);
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
  void _apply(Uint8List bytes) {
    _applying = true;
    try {
      _controller.write(bytes);
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
          if (widget.onOpenChat != null)
            IconButton(
              tooltip: '对话',
              onPressed: widget.onOpenChat,
              icon: const Icon(Icons.forum_outlined),
            ),
          PopupMenuButton<String>(
            tooltip: '更多',
            icon: const Icon(Icons.more_horiz_rounded),
            onSelected: (value) async {
              if (value != 'close') return;
              final nav = Navigator.of(context);
              if (await closeSession(context, widget.model, widget.tab)) {
                nav.pop();
              }
            },
            itemBuilder: (_) => [
              PopupMenuItem(
                value: 'close',
                child: Text(widget.tab.agent == null ? '关闭终端' : '关闭会话'),
              ),
            ],
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
              child: TerminalView(
                controller: _controller,
                theme: terminalTheme(
                  c,
                  fontSize: widget.model.settings.terminalFontSize,
                ),
                // 进来先看画面，点一下终端才弹键盘，不然键盘挡住半屏
                autofocus: false,
                focusNode: _focus,
                padding: EdgeInsets.zero,
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
