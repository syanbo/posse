import 'dart:async';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import '../workspace/models.dart';
import 'clipboard_paste.dart';
import 'image_pick.dart';
import 'image_upload.dart';
import 'merge.dart';
import 'permission.dart';
import 'prompt.dart';
import 'send.dart';
import 'transcript.dart';
import '../files/files.dart';
import '../workspace/session_actions.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'bubbles.dart';
import 'cards.dart';
import 'composer.dart';
import 'echo.dart';
import 'session_bar.dart';

export 'bubbles.dart';
export 'cards.dart';
export 'chat_style.dart';
export 'session_bar.dart';

class ChatScreen extends StatefulWidget {
  const ChatScreen({
    super.key,
    required this.model,
    required this.tab,
    this.onOpenTerminal,
  });

  final HiveAppModel model;
  final SessionTab tab;

  /// 打开终端视图。放在外面，避免对话页依赖原生终端插件。
  final VoidCallback? onOpenTerminal;

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final _window = ChatWindow();
  final _input = TextEditingController();
  final _scroll = ScrollController();
  SessionTab? _tab;
  RpcStream? _chat;
  RpcStream? _tabs;
  String? _chatSubId;
  PhoneRpc? _bound;
  var _perm = const LivePermission();
  var _sending = false;
  var _loadingEarlier = false;

  /// 选好、还没发出去的图片。发出去时按顺序存到电脑的临时目录，再把路径写进终端。
  final _pending = <Uint8List>[];

  /// 已经传到电脑上的图片和路径。发送失败后重发不再传一遍。
  final _uploaded = <Uint8List, String>{};

  /// 发出去、会话文件里还没出现的话。
  final _echoes = LocalEchoes();

  /// 电脑推来的 agent 状态行（终端底部转圈那一行），没有时为空。
  String? _status;

  /// 这个会话的终端已经不在了：输入框停用，顶上说一声。
  var _ended = false;
  var _gone = false;
  var _openGen = 0;
  Future<void> _gate = Future<void>.value();
  String? _notice;

  SessionTab get tab => _tab ?? widget.tab;

  PhoneRpc? get _rpc => widget.model.rpc;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _scroll.addListener(_onScroll);
    _scheduleOpen();
  }

  /// 滚到顶附近就读更早的消息，不用另外点按钮。
  void _onScroll() {
    if (!_scroll.hasClients || !_landed) return;
    final position = _scroll.position;
    if (position.pixels <= position.minScrollExtent + 120) {
      unawaited(_loadEarlier());
    }
  }

  void _onModel() {
    if (!mounted) return;
    if (widget.model.rpc != _bound) _scheduleOpen();
  }

  void _scheduleOpen() {
    _gate = _gate.then((_) => _open()).catchError((Object _) {});
  }

  @override
  void dispose() {
    _gone = true;
    _openGen++;
    widget.model.removeListener(_onModel);
    _drop(_bound, chatSubId: _chatSubId, tabsId: _tabs?.id);
    unawaited(_chat?.cancel());
    unawaited(_tabs?.cancel());
    _input.dispose();
    _scroll.dispose();
    super.dispose();
  }

  void _drop(PhoneRpc? rpc, {String? chatSubId, String? tabsId}) {
    if (rpc == null) return;
    if (chatSubId != null) {
      unawaited(
        rpc
            .call('nativeChat.unsubscribe', {'subscriptionId': chatSubId})
            .catchError((Object _) => <String, Object?>{}),
      );
    }
    if (tabsId != null) {
      unawaited(
        rpc
            .call('session.tabs.unsubscribe', {'subscriptionId': tabsId})
            .catchError((Object _) => <String, Object?>{}),
      );
    }
  }

  Future<void> _open() async {
    final rpc = _rpc;
    if (!tab.eligible || _gone || rpc == null || identical(rpc, _bound)) return;
    final gen = ++_openGen;
    final previous = _bound;
    final previousChat = _chatSubId;
    final previousTabs = _tabs?.id;
    await _chat?.cancel();
    await _tabs?.cancel();
    _chat = null;
    _tabs = null;
    _drop(previous, chatSubId: previousChat, tabsId: previousTabs);
    if (gen != _openGen || _gone) return;
    final subId = '${tab.agent}:${tab.sessionId}:live';
    _chatSubId = subId;
    _status = null;
    try {
      final chat = await rpc.subscribe('nativeChat.subscribe', {
        'agent': tab.agent,
        'sessionId': tab.sessionId,
        if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
        'limit': 40,
        'subscriptionId': subId,
        'capabilities': {'transcriptPending': 1},
      });
      if (gen != _openGen || _gone) {
        _drop(rpc, chatSubId: subId);
        await chat.cancel();
        return;
      }
      final tabs = await rpc.subscribe('session.tabs.subscribe', {
        'worktree': tab.worktreeId,
      });
      if (gen != _openGen || _gone) {
        _drop(rpc, chatSubId: subId, tabsId: tabs.id);
        await chat.cancel();
        await tabs.cancel();
        return;
      }
      _bound = rpc;
      _chat = chat;
      _tabs = tabs;
      chat.frames.listen((frame) {
        if (!mounted || gen != _openGen) return;
        if (frame['type'] == 'permission') {
          final id = frame['requestId'];
          if (id is! String || id.isEmpty) return;
          setState(() {
            _perm = _perm.receive(
              requestId: id,
              tool: frame['tool'] as String? ?? '',
              input: frame['input'],
              alreadyWaiting: tab.state == 'waiting',
            );
          });
          return;
        }
        if (frame['type'] == 'status') {
          final text = frame['text'];
          final next = text is String && text.isNotEmpty ? text : null;
          if (next != _status) setState(() => _status = next);
          return;
        }
        setState(() {
          _window.applyFrame(frame);
          _echoes.reconcile(_window.messages);
        });
        _stickToBottom();
      });
      tabs.frames.listen((frame) {
        if (!mounted || gen != _openGen) return;
        final nextTabs = tabsFromFrame(tab.worktreeId, frame);
        if (nextTabs == null) return;
        final alive = nextTabs.any((t) => t.terminalId == tab.terminalId);
        if (alive == _ended) setState(() => _ended = !alive);
        for (final next in nextTabs) {
          if (next.terminalId != tab.terminalId) continue;
          setState(() {
            _tab = next;
            _perm = _perm.observe(waiting: next.state == 'waiting');
          });
        }
      });
    } catch (error) {
      if (gen != _openGen || _gone) return;
      _bound = null;
      final tabsId = _tabs?.id;
      await _chat?.cancel();
      await _tabs?.cancel();
      _chat = null;
      _tabs = null;
      _drop(rpc, chatSubId: subId, tabsId: tabsId);
      if (mounted) setState(() => _notice = '$error');
    }
  }

  /// 第一次拿到消息时直接跳到最新一条；之后只在已经看着底部时跟着往下。
  var _landed = false;

  /// [force]：自己刚发了话，不管停在哪都回到底部。
  void _stickToBottom({bool force = false}) {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_scroll.hasClients) return;
      final position = _scroll.position;
      if (!_landed) {
        if (_window.messages.isEmpty) return;
        _landed = true;
        _scroll.jumpTo(position.maxScrollExtent);
        // 内容高度要等图片、代码块排完才稳定，下一帧再贴一次底
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (_scroll.hasClients) {
            _scroll.jumpTo(_scroll.position.maxScrollExtent);
          }
        });
        return;
      }
      if (force || position.maxScrollExtent - position.pixels < 80) {
        _scroll.jumpTo(position.maxScrollExtent);
      }
      // 消息不满一屏或者停在顶部附近时，不会再有滚动来触发，直接接着读
      if (position.pixels <= position.minScrollExtent + 120) {
        unawaited(_loadEarlier());
      }
    });
  }

  Future<void> _loadEarlier() async {
    final before = _window.beforeOffset;
    if (_loadingEarlier ||
        !_window.hasMore ||
        before == null ||
        !tab.eligible) {
      return;
    }
    setState(() => _loadingEarlier = true);
    try {
      final current = _rpc;
      if (current == null) return;
      final page = await current.call('nativeChat.readSession', {
        'agent': tab.agent,
        'sessionId': tab.sessionId,
        if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
        'limit': 40,
        'beforeOffset': before,
      });
      if (!mounted) return;
      // 更早的消息加在上面，内容整体变高；按高度差往下挪，正在看的那条留在原处
      final oldExtent = _scroll.hasClients
          ? _scroll.position.maxScrollExtent
          : 0.0;
      setState(() => _window.applyOlderPage(page));
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!_scroll.hasClients) return;
        final grown = _scroll.position.maxScrollExtent - oldExtent;
        if (grown > 0) _scroll.jumpTo(_scroll.position.pixels + grown);
      });
    } catch (error) {
      if (mounted) setState(() => _notice = '$error');
    } finally {
      if (mounted) setState(() => _loadingEarlier = false);
    }
  }

  Future<SendOutcome> _send(String text, {required bool enter}) async {
    final current = _rpc;
    if (current == null) return SendOutcome.unknown;
    final outcome = await sendTerminal(
      current,
      terminal: tab.terminalId,
      text: text,
      enter: enter,
    );
    final note = sendOutcomeText(outcome);
    if (!mounted) return outcome;
    setState(() {
      _notice = note.isEmpty ? null : note;
      if (outcome == SendOutcome.accepted && enter && text == _input.text) {
        _input.clear();
      }
    });
    return outcome;
  }

  void _addImages(List<Uint8List> images) {
    if (!mounted || images.isEmpty) return;
    final room = maxChatImages - _pending.length;
    setState(() {
      _pending.addAll(images.take(room));
      if (images.length > room) _notice = '一次最多 $maxChatImages 张';
    });
  }

  void _insertText(String text) {
    final value = _input.value;
    final selection = value.selection;
    final start = selection.isValid ? selection.start : value.text.length;
    final end = selection.isValid ? selection.end : value.text.length;
    final next = value.text.replaceRange(start, end, text);
    _input.value = TextEditingValue(
      text: next,
      selection: TextSelection.collapsed(offset: start + text.length),
    );
    setState(() {});
  }

  /// 文字插进输入框，图片加进待发列表。两边都没有就告诉用户。
  Future<void> _pasteClipboard() async {
    if (_sending) return;
    try {
      final paste = await readClipboardPaste();
      if (!mounted) return;
      if (paste.isText) {
        _insertText(paste.text!);
        return;
      }
      if (paste.isImage) {
        _addImages([paste.image!]);
        return;
      }
      setState(() => _notice = '剪贴板里没有文字或图片');
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error));
    }
  }

  void _onContentInserted(KeyboardInsertedContent content) {
    final data = content.data;
    if (data == null ||
        data.isEmpty ||
        !content.mimeType.startsWith('image/')) {
      return;
    }
    _addImages([data]);
  }

  Future<void> _pickImage() async {
    if (_rpc == null || _sending) return;
    try {
      final ask = await askImageSource(context, clipboard: true);
      if (ask == null || !mounted) return;
      switch (ask) {
        case ImageAsk.gallery:
          final room = maxChatImages - _pending.length;
          if (room < 1) {
            setState(() => _notice = '一次最多 $maxChatImages 张');
            return;
          }
          _addImages(await pickGalleryImages(limit: room));
        case ImageAsk.camera:
          final one = await pickOneImage(ImageSource.camera);
          if (one != null) _addImages([one]);
        case ImageAsk.clipboard:
          await _pasteClipboard();
      }
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error));
    }
  }

  /// 有图就按顺序全部上传，说明和路径一次提交。任何一张失败，输入和图片都留着。
  Future<void> _submit() async {
    final current = _rpc;
    final images = List<Uint8List>.from(_pending);
    final caption = _input.text;
    if (current == null || _sending) return;
    if (caption.trim().isEmpty && images.isEmpty) return;
    // 斜杠命令 agent 不记成用户消息，等不到对应的那条，不先画
    final echo = caption.trimLeft().startsWith('/') && images.isEmpty
        ? null
        : _echoes.add(caption.trim(), images: images.length);
    setState(() {
      _sending = true;
      // 输入框马上清空，话先画在对话末尾；没发出去再放回来
      _input.clear();
      if (images.isNotEmpty) _notice = '正在发送图片…';
    });
    _stickToBottom(force: true);
    var accepted = false;
    try {
      var text = caption;
      if (images.isNotEmpty) {
        final paths = <String>[];
        for (final image in images) {
          paths.add(_uploaded[image] ??= await uploadImage(current, image));
        }
        text = imageMessage(caption, paths);
      }
      accepted = await _send(text, enter: true) == SendOutcome.accepted;
      if (!mounted) return;
      if (accepted) {
        setState(() {
          echo?.sent = true;
          // 上传期间新加的图片留着，只去掉发出去的
          _pending.removeRange(0, images.length);
          for (final image in images) {
            _uploaded.remove(image);
          }
        });
      }
    } catch (error) {
      if (mounted) setState(() => _notice = friendlyError(error));
    } finally {
      if (mounted) {
        setState(() {
          _sending = false;
          if (!accepted) {
            if (echo != null) _echoes.remove(echo);
            if (_input.text.isEmpty) _input.text = caption;
          }
        });
      }
    }
  }

  Future<void> _decide(String key) async {
    setState(() => _sending = true);
    final id = _perm.requestId;
    final behavior = permissionBehavior(key);
    var decided = false;
    if (id != null && behavior != null) {
      final current = _rpc;
      if (current != null) {
        try {
          await current.call('agent.permission.decide', {
            'requestId': id,
            'behavior': behavior,
          });
          decided = true;
          if (mounted) setState(() => _perm = _perm.settle());
        } catch (_) {
          decided = false;
        }
      }
    }
    if (!decided) await _send(key, enter: false);
    if (mounted) setState(() => _sending = false);
  }

  Approval? _visibleApproval() {
    if (_perm.settled) return null;
    final parsed = parseInteractivePrompt(tab.prompt)?.approval;
    if (_perm.requestId == null) return parsed;
    final tool = _perm.tool.isNotEmpty ? _perm.tool : (parsed?.tool ?? '操作');
    final summary = parsed != null && parsed.summary.isNotEmpty
        ? parsed.summary
        : permissionSummary(_perm.input);
    return Approval(tool, summary);
  }

  Future<void> _answer(List<String> lines) async {
    setState(() => _sending = true);
    await runAnswerSteps(
      lines: lines,
      writeBody: (line) async {
        await _send(line, enter: false);
      },
      writeEnter: () async {
        await _send('', enter: true);
      },
    );
    if (mounted) setState(() => _sending = false);
  }

  @override
  Widget build(BuildContext context) {
    final prompt = parseInteractivePrompt(tab.prompt);
    final approval = _visibleApproval();
    final canvas = Theme.of(context).scaffoldBackgroundColor;
    final settings = widget.model.settings;
    final entries = foldTranscript(_window.messages)
        .where((e) => settings.showReasoning || e.message?.role != 'reasoning')
        .toList();
    final c = HiveColors.of(context);
    // agent 在干活时对话最后一行转着星号，看得出它还在；停下、断线或会话结束就不画
    final echoes = _echoes.items;
    final working =
        !_ended &&
        widget.model.rpc != null &&
        (agentStateOf(tab.state) == AgentState.working ||
            (_status?.isNotEmpty ?? false));
    return Scaffold(
      backgroundColor: canvas,
      appBar: AppBar(
        titleSpacing: 0,
        title: SessionTitle(tab: tab, model: widget.model, ended: _ended),
        actions: [
          FilesButton(
            model: widget.model,
            worktree: tab.worktreeId,
            title: worktreePath(widget.model, tab.worktreeId),
          ),
          // 切到终端放进"更多"里，和终端页对称
          PopupMenuButton<String>(
            tooltip: '更多',
            icon: const Icon(Icons.more_horiz_rounded),
            onSelected: (value) async {
              if (value == 'terminal') {
                widget.onOpenTerminal?.call();
                return;
              }
              if (value != 'close') return;
              final nav = Navigator.of(context);
              if (await closeSession(context, widget.model, tab)) nav.pop();
            },
            itemBuilder: (_) => [
              if (widget.onOpenTerminal != null)
                const PopupMenuItem(value: 'terminal', child: Text('切换到终端')),
              const PopupMenuItem(value: 'close', child: Text('关闭会话')),
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
          if (!tab.eligible)
            const Expanded(child: Center(child: Text('这个会话没有原生存档')))
          else ...[
            if (_window.pending && _window.messages.isEmpty)
              const LinearProgressIndicator(minHeight: 2),
            if (_window.error != null) NoticeBar(_window.error!),
            Expanded(
              child: SelectionArea(
                child: Scrollbar(
                  controller: _scroll,
                  child: ListView.builder(
                    controller: _scroll,
                    padding: const EdgeInsets.fromLTRB(0, 8, 0, 24),
                    // 还有更早的消息时，列表最上面多一行：滚到顶才看得见，读的时候转圈
                    itemCount:
                        entries.length +
                        echoes.length +
                        (_window.hasMore ? 1 : 0) +
                        (working ? 1 : 0),
                    itemBuilder: (context, index) {
                      if (_window.hasMore) {
                        if (index == 0) return _earlierRow(c);
                        index--;
                      }
                      if (index >= entries.length) {
                        final at = index - entries.length;
                        if (at < echoes.length) {
                          return EchoBubble(echo: echoes[at]);
                        }
                        return WorkingRow(status: _status, tool: tab.toolName);
                      }
                      final entry = entries[index];
                      if (entry.tools) {
                        return ToolRun(
                          key: ValueKey(toolKey(entry.blocks, index)),
                          blocks: entry.blocks,
                          initiallyOpen: settings.expandTools,
                        );
                      }
                      return MessageBubble(
                        message: entry.message!,
                        blocks: entry.blocks,
                        agent: tab.agent,
                      );
                    },
                  ),
                ),
              ),
            ),
          ],
          if (approval != null)
            ApprovalCard(
              approval: approval,
              busy: _sending,
              onDecision: _decide,
            )
          else if (prompt != null && prompt.questions.isNotEmpty && !_sending)
            QuestionCard(questions: prompt.questions, onSubmit: _answer),
          if (_notice != null) NoticeBar(_notice!),
          ChatComposer(
            controller: _input,
            images: _pending,
            sending: _sending,
            connected: _rpc != null,
            ended: _ended,
            hint: '回复 ${chatWho('assistant', tab.agent)}',
            onChanged: (_) => setState(() {}),
            onSubmit: _submit,
            onPickImage: _pickImage,
            onRemoveImage: (index) =>
                setState(() => _uploaded.remove(_pending.removeAt(index))),
            onPaste: () => unawaited(_pasteClipboard()),
            onContentInserted: _onContentInserted,
          ),
        ],
      ),
    );
  }

  Widget _earlierRow(HiveColors c) {
    return SizedBox(
      height: 44,
      child: Center(
        child: _loadingEarlier
            ? const SizedBox.square(
                dimension: 18,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            : TextButton(
                style: TextButton.styleFrom(
                  minimumSize: const Size(0, 30),
                  padding: const EdgeInsets.symmetric(horizontal: 12),
                  backgroundColor: Colors.transparent,
                  foregroundColor: c.muted,
                  textStyle: const TextStyle(fontSize: 13),
                ),
                onPressed: _loadEarlier,
                child: const Text('更早的消息'),
              ),
      ),
    );
  }
}
