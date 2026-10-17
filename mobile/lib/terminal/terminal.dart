import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flterm/flterm.dart' hide TerminalScreen;
import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/send.dart';
import '../protocol/rpc.dart';
import '../workspace/models.dart';
import 'frames.dart';

/// 终端视图。单测不要 import 这个文件：flterm 带原生库，宿主 VM 测试不该编它。
class TerminalScreen extends StatefulWidget {
  const TerminalScreen({super.key, required this.model, required this.tab});

  final HiveAppModel model;
  final SessionTab tab;

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
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && widget.model.rpc != _bound) {
      _schedule();
    }
  }

  void _onModel() {
    if (widget.model.rpc != _bound) _schedule();
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
    await _detach();
    if (gen != _gen || !mounted) return;
    // 先听二进制流，再发订阅，快照帧才不会在监听挂上之前过去。
    _binary = rpc.binary.listen(
      (bytes) {
        if (!mounted || gen != _gen) return;
        final piece = _asm.add(bytes);
        if (piece == null) return;
        _apply(piece.bytes);
      },
      onDone: () {
        if (gen != _gen) return;
        _stream = null;
        _bound = null;
      },
    );
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
        if (frame['type'] == 'end') {
          setState(() => _notice = '终端已经退出');
        }
      });
    } catch (error) {
      if (gen != _gen || !mounted) return;
      await _binary?.cancel();
      _binary = null;
      setState(() => _notice = '$error');
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

  Future<void> _send(List<int> bytes) async {
    final rpc = widget.model.rpc;
    if (rpc == null || bytes.isEmpty) return;
    final outcome = await sendTerminal(
      rpc,
      terminal: widget.tab.terminalId,
      text: latin1.decode(bytes),
      enter: false,
    );
    final note = sendOutcomeText(outcome);
    if (!mounted || note.isEmpty) return;
    setState(() => _notice = note);
  }

  @override
  Widget build(BuildContext context) {
    final dark = Theme.of(context).brightness == Brightness.dark;
    return Scaffold(
      appBar: AppBar(title: Text(widget.tab.title)),
      body: Column(
        children: [
          if (_notice != null)
            Material(
              color: Theme.of(context).colorScheme.errorContainer,
              child: Padding(
                padding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 6,
                ),
                child: Text(_notice!),
              ),
            ),
          Expanded(
            child: TerminalView(
              controller: _controller,
              theme: dark ? TerminalTheme.dark() : TerminalTheme.light(),
              autofocus: true,
              padding: EdgeInsets.zero,
            ),
          ),
          _KeyBar(onKey: _send),
        ],
      ),
    );
  }
}

class _KeyBar extends StatelessWidget {
  const _KeyBar({required this.onKey});

  final Future<void> Function(List<int> bytes) onKey;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: Theme.of(context).colorScheme.surfaceContainerLow,
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: 48,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
            children: [
              for (final key in termKeys)
                Padding(
                  padding: const EdgeInsets.symmetric(horizontal: 4),
                  child: OutlinedButton(
                    onPressed: () => onKey(key.bytes),
                    child: Text(key.label),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}
