import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import '../workspace/models.dart';
import 'markdown_view.dart';
import 'merge.dart';
import 'models.dart';
import 'permission.dart';
import 'prompt.dart';
import 'send.dart';
import 'transcript.dart';

const _accentLight = Color(0xFFC15F3C);
const _accentDark = Color(0xFFD97757);
const _mutedLight = Color(0xFF6F6B66);
const _mutedDark = Color(0xFF9C9893);

Color chatAccent(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? _accentDark
    : _accentLight;

Color chatMuted(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark ? _mutedDark : _mutedLight;

/// 自己的话用强调色淡底。深色 20%、浅色 12%，和桌面历史详情的 tint 一致。
Color chatTint(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0x33D97757)
    : const Color(0x1FC15F3C);

Color chatCard(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0xFF2A2A27)
    : Colors.white;

class ApprovalCard extends StatelessWidget {
  const ApprovalCard({
    super.key,
    required this.approval,
    required this.busy,
    required this.onDecision,
  });

  final Approval approval;
  final bool busy;
  final void Function(String key) onDecision;

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: chatCard(context),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            '允许 ${approval.tool}？',
            style: const TextStyle(fontWeight: FontWeight.w600),
          ),
          if (approval.summary.isNotEmpty) ...[
            const SizedBox(height: 6),
            Text(approval.summary, style: TextStyle(color: chatMuted(context))),
          ],
          const SizedBox(height: 10),
          Row(
            children: [
              FilledButton(
                onPressed: busy ? null : () => onDecision(allowKey),
                child: const Text('允许'),
              ),
              const SizedBox(width: 8),
              TextButton(
                onPressed: busy ? null : () => onDecision(denyKey),
                child: const Text('拒绝'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class QuestionCard extends StatefulWidget {
  const QuestionCard({
    super.key,
    required this.questions,
    required this.onSubmit,
  });

  final List<AskQuestion> questions;
  final void Function(List<String> lines) onSubmit;

  @override
  State<QuestionCard> createState() => _QuestionCardState();
}

class _QuestionCardState extends State<QuestionCard> {
  final _selected = <int, Set<String>>{};
  final _other = <int, TextEditingController>{};

  @override
  void dispose() {
    for (final controller in _other.values) {
      controller.dispose();
    }
    super.dispose();
  }

  TextEditingController _otherOf(int index) =>
      _other.putIfAbsent(index, TextEditingController.new);

  bool get _ready {
    for (var i = 0; i < widget.questions.length; i++) {
      final other = _other[i]?.text.trim() ?? '';
      if ((_selected[i]?.isNotEmpty ?? false) || other.isNotEmpty) continue;
      return false;
    }
    return widget.questions.isNotEmpty;
  }

  void _submit() {
    final lines = <String>[];
    for (var i = 0; i < widget.questions.length; i++) {
      final other = _other[i]?.text.trim() ?? '';
      if (other.isNotEmpty) {
        lines.add(other);
        continue;
      }
      lines.add(formatAnswer(widget.questions[i], [...?_selected[i]]));
    }
    widget.onSubmit(lines);
  }

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: chatCard(context),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          for (var i = 0; i < widget.questions.length; i++) ...[
            if (widget.questions[i].header != null)
              Text(
                widget.questions[i].header!,
                style: TextStyle(color: chatAccent(context), fontSize: 12),
              ),
            Text(
              widget.questions[i].question,
              style: const TextStyle(fontWeight: FontWeight.w600),
            ),
            const SizedBox(height: 6),
            Wrap(
              spacing: 6,
              runSpacing: 6,
              children: [
                for (final option in widget.questions[i].options)
                  FilterChip(
                    label: Text(option.label),
                    selectedColor: chatTint(context),
                    checkmarkColor: chatAccent(context),
                    selected: _selected[i]?.contains(option.label) ?? false,
                    onSelected: (selected) => setState(() {
                      final set = _selected.putIfAbsent(i, () => {});
                      if (widget.questions[i].multiSelect) {
                        selected
                            ? set.add(option.label)
                            : set.remove(option.label);
                      } else {
                        set
                          ..clear()
                          ..add(option.label);
                        _otherOf(i).clear();
                      }
                    }),
                  ),
              ],
            ),
            TextField(
              controller: _otherOf(i),
              decoration: const InputDecoration(hintText: '其他', isDense: true),
              onChanged: (_) => setState(() {}),
            ),
            const SizedBox(height: 8),
          ],
          Align(
            alignment: Alignment.centerRight,
            child: FilledButton(
              onPressed: _ready ? _submit : null,
              child: const Text('提交'),
            ),
          ),
        ],
      ),
    );
  }
}

class MessageBubble extends StatelessWidget {
  const MessageBubble({
    super.key,
    required this.message,
    required this.blocks,
    required this.agent,
  });

  final ChatMessage message;
  final List<ChatBlock> blocks;
  final String? agent;

  @override
  Widget build(BuildContext context) {
    final mine = message.role == 'user';
    final width = MediaQuery.sizeOf(context).width;
    final muted = chatMuted(context);
    final who = chatWho(message.role, agent);
    final time = chatTimeLabel(message.timestamp);
    final body = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text(
              who,
              style: TextStyle(
                fontSize: 12,
                fontWeight: FontWeight.w500,
                color: mine ? chatAccent(context) : muted,
              ),
            ),
            if (time != null) ...[
              const SizedBox(width: 8),
              Text(
                time,
                style: TextStyle(
                  fontSize: 11,
                  color: muted,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ],
        ),
        const SizedBox(height: 6),
        for (final block in blocks) _block(context, message.role, block),
      ],
    );
    // 回复直接铺在底上。只有自己的话靠右、强调色淡底。
    if (!mine) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(16, 4, 16, 10),
        child: body,
      );
    }
    return Align(
      alignment: Alignment.centerRight,
      child: Container(
        constraints: BoxConstraints(maxWidth: width * 0.8),
        margin: const EdgeInsets.fromLTRB(48, 4, 16, 10),
        padding: const EdgeInsets.fromLTRB(16, 10, 16, 12),
        decoration: BoxDecoration(
          color: chatTint(context),
          borderRadius: BorderRadius.circular(10),
        ),
        child: body,
      ),
    );
  }

  Widget _block(BuildContext context, String role, ChatBlock block) {
    final muted = chatMuted(context);
    if (block.type == 'image-ref') {
      return Text(
        block.path ?? block.url ?? '图片',
        style: TextStyle(fontSize: 13, color: muted),
      );
    }
    final text = block.text ?? '';
    if (role == 'user') {
      return Text(text, style: const TextStyle(fontSize: 15, height: 1.55));
    }
    final style = role == 'reasoning'
        ? TextStyle(fontStyle: FontStyle.italic, color: muted, height: 1.55)
        : role == 'system'
        ? TextStyle(fontSize: 13, color: muted, height: 1.5)
        : const TextStyle(fontSize: 15, height: 1.55);
    return MarkdownView(text, style: style);
  }
}

class ToolRun extends StatefulWidget {
  const ToolRun({super.key, required this.blocks});

  final List<ChatBlock> blocks;

  @override
  State<ToolRun> createState() => _ToolRunState();
}

class _ToolRunState extends State<ToolRun> {
  var _open = false;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    final count = toolRunCount(widget.blocks);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => setState(() => _open = !_open),
            child: Padding(
              padding: const EdgeInsets.symmetric(vertical: 4),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(
                    _open ? Icons.expand_more : Icons.chevron_right,
                    size: 16,
                    color: muted,
                  ),
                  Text(
                    '工具调用 $count 次',
                    style: TextStyle(fontSize: 12, color: muted),
                  ),
                ],
              ),
            ),
          ),
          if (_open)
            for (final block in widget.blocks) _toolBody(context, block, muted),
        ],
      ),
    );
  }

  Widget _toolBody(BuildContext context, ChatBlock block, Color muted) {
    final title = block.isError
        ? '工具失败'
        : (block.name?.isNotEmpty ?? false)
        ? block.name!
        : '工具';
    final detail = block.type == 'tool-result'
        ? (block.output ?? '')
        : _preview(block.input);
    return Padding(
      padding: const EdgeInsets.only(left: 18, bottom: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            title,
            style: TextStyle(
              fontSize: 12,
              fontWeight: FontWeight.w500,
              color: block.isError
                  ? Theme.of(context).colorScheme.error
                  : muted,
            ),
          ),
          if (detail.isNotEmpty)
            Text(
              detail,
              maxLines: 8,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontFamily: 'monospace',
                fontSize: 12,
                height: 1.45,
                color: muted,
              ),
            ),
        ],
      ),
    );
  }
}

String _preview(Object? input) {
  if (input == null) return '';
  if (input is String) return input;
  try {
    return const JsonEncoder.withIndent('  ').convert(input);
  } catch (_) {
    return '$input';
  }
}

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
    _scheduleOpen();
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
        setState(() => _window.applyFrame(frame));
        _stickToBottom();
      });
      tabs.frames.listen((frame) {
        if (!mounted || gen != _openGen) return;
        final nextTabs = tabsFromFrame(tab.worktreeId, frame);
        if (nextTabs == null) return;
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

  void _stickToBottom() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_scroll.hasClients) return;
      final position = _scroll.position;
      if (position.maxScrollExtent - position.pixels < 80) {
        _scroll.jumpTo(position.maxScrollExtent);
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
      setState(() => _window.applyOlderPage(page));
    } catch (error) {
      if (mounted) setState(() => _notice = '$error');
    } finally {
      if (mounted) setState(() => _loadingEarlier = false);
    }
  }

  Future<void> _send(String text, {required bool enter}) async {
    final current = _rpc;
    if (current == null) return;
    final outcome = await sendTerminal(
      current,
      terminal: tab.terminalId,
      text: text,
      enter: enter,
    );
    final note = sendOutcomeText(outcome);
    if (!mounted) return;
    setState(() {
      _notice = note.isEmpty ? null : note;
      if (outcome == SendOutcome.accepted && enter && text == _input.text) {
        _input.clear();
      }
    });
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
    final entries = foldTranscript(_window.messages);
    return Scaffold(
      backgroundColor: canvas,
      appBar: AppBar(
        title: Text(tab.title),
        backgroundColor: canvas,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        actions: [
          if (widget.onOpenTerminal != null)
            IconButton(
              tooltip: '终端',
              onPressed: widget.onOpenTerminal,
              icon: const Icon(Icons.terminal),
            ),
        ],
      ),
      body: Column(
        children: [
          if (!tab.eligible)
            const Expanded(child: Center(child: Text('这个会话没有原生存档')))
          else ...[
            if (_window.hasMore)
              TextButton(
                onPressed: _loadingEarlier ? null : _loadEarlier,
                child: const Text('更早的消息'),
              ),
            if (_window.pending && _window.messages.isEmpty)
              const LinearProgressIndicator(minHeight: 2),
            if (_window.error != null)
              Padding(
                padding: const EdgeInsets.all(8),
                child: Text(
                  _window.error!,
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            Expanded(
              child: SelectionArea(
                child: Scrollbar(
                  controller: _scroll,
                  child: ListView.builder(
                    controller: _scroll,
                    padding: const EdgeInsets.fromLTRB(0, 8, 0, 24),
                    itemCount: entries.length,
                    itemBuilder: (context, index) {
                      final entry = entries[index];
                      if (entry.tools) {
                        return ToolRun(
                          key: ValueKey(_toolKey(entry.blocks, index)),
                          blocks: entry.blocks,
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
          if (_notice != null)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: Text(
                _notice!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ),
          _composer(),
        ],
      ),
    );
  }

  Widget _composer() {
    final dark = Theme.of(context).brightness == Brightness.dark;
    final ready = !_sending && _input.text.trim().isNotEmpty;
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 4, 8, 8),
        child: DecoratedBox(
          decoration: BoxDecoration(
            color: dark ? const Color(0xFF2A2A27) : Colors.white,
            borderRadius: BorderRadius.circular(10),
          ),
          child: Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _input,
                  minLines: 1,
                  maxLines: 5,
                  onChanged: (_) => setState(() {}),
                  decoration: const InputDecoration(
                    hintText: '发消息',
                    border: InputBorder.none,
                    isDense: true,
                    contentPadding: EdgeInsets.fromLTRB(14, 10, 4, 10),
                  ),
                ),
              ),
              IconButton(
                onPressed: ready ? () => _send(_input.text, enter: true) : null,
                color: chatAccent(context),
                icon: const Icon(Icons.send),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

String _toolKey(List<ChatBlock> blocks, int index) {
  final ids = blocks
      .map((block) => block.callId)
      .whereType<String>()
      .where((id) => id.isNotEmpty)
      .join(',');
  if (ids.isNotEmpty) return ids;
  return 'tools-$index-${blocks.length}';
}
