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
import '../files/files.dart';
import '../workspace/session_actions.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

Color chatAccent(BuildContext context) => HiveColors.of(context).accent;

Color chatMuted(BuildContext context) => HiveColors.of(context).muted;

/// 自己的话用强调色淡底。深色 20%、浅色 12%，和桌面历史详情的 tint 一致。
Color chatTint(BuildContext context) => HiveColors.of(context).tint;

Color chatCard(BuildContext context) => HiveColors.of(context).card;

/// 等你回应的卡片（批准、提问）：卡片底加琥珀色细边，是界面上唯一醒目的东西。
BoxDecoration _askCard(HiveColors c) => BoxDecoration(
  color: c.card,
  borderRadius: BorderRadius.circular(Rd.card),
  border: Border.all(color: c.honey.withValues(alpha: 0.5)),
);

/// 会话页和终端页共用的顶栏标题：agent 图标、标题，下面一行是状态。
class SessionTitle extends StatelessWidget {
  const SessionTitle({
    super.key,
    required this.tab,
    this.model,
    this.ended = false,
  });

  final SessionTab tab;

  /// 给了就在 agent 图标右下角画连接状态的小圆点：绿色连着，琥珀色断开或正在重连。
  final HiveAppModel? model;

  @override
  Widget build(BuildContext context) {
    final model = this.model;
    if (model == null) return _content(context, offline: false, ended: false);
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) => _content(
        context,
        offline: model.rpc == null || model.connecting,
        ended: ended,
      ),
    );
  }

  /// 会话的终端已经不在了（关掉了，或者常驻进程重启过）。
  final bool ended;

  Widget _content(
    BuildContext context, {
    required bool offline,
    required bool ended,
  }) {
    final c = HiveColors.of(context);
    // 断线时手上的状态已经过时，不显示；会话结束了也没有状态可言
    final state = offline || ended ? AgentState.none : agentStateOf(tab.state);
    final stateText = ended
        ? '会话已经结束'
        : offline
        ? '等连上电脑'
        : switch (state) {
            AgentState.working =>
              tab.toolName == null ? '工作中' : '正在用 ${tab.toolName}',
            AgentState.waiting => '等你处理',
            AgentState.done => '本轮完成',
            _ => tab.agent == null ? '终端' : chatWho('assistant', tab.agent),
          };
    return Row(
      children: [
        _withLink(c, AgentTile(agent: tab.agent, size: 28)),
        const SizedBox(width: 10),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                cleanTitle(tab.title),
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.w600,
                  color: c.text,
                ),
              ),
              Row(
                children: [
                  // 没有状态（普通终端）时不留空位，文字和标题左边对齐
                  if (state != AgentState.none) ...[
                    StatusHex(state: state, size: 11),
                    const SizedBox(width: 4),
                  ],
                  Flexible(
                    child: Text(
                      stateText,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        fontSize: 12,
                        fontWeight: FontWeight.w400,
                        color: state == AgentState.waiting ? c.honey : c.muted,
                      ),
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ],
    );
  }
}

extension on SessionTitle {
  Widget _withLink(HiveColors c, Widget tile) {
    final model = this.model;
    if (model == null) return tile;
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final online = model.rpc != null && !model.connecting;
        return Stack(
          clipBehavior: Clip.none,
          children: [
            tile,
            Positioned(
              right: -3,
              bottom: -3,
              child: Tooltip(
                message: online ? '已连接电脑' : '和电脑的连接断开了',
                child: Container(
                  width: 11,
                  height: 11,
                  decoration: BoxDecoration(
                    color: online ? c.sage : c.honey,
                    shape: BoxShape.circle,
                    border: Border.all(color: c.canvas, width: 2),
                  ),
                ),
              ),
            ),
          ],
        );
      },
    );
  }
}

/// 和电脑断开、正在重连时，顶栏下面的一条提示。连着时不占地方。
class ConnectionBanner extends StatelessWidget {
  const ConnectionBanner({super.key, required this.model});

  final HiveAppModel model;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        if (model.rpc != null && !model.connecting) {
          return const SizedBox.shrink();
        }
        final c = HiveColors.of(context);
        final name = model.current?.label ?? '电脑';
        return Container(
          width: double.infinity,
          color: c.honey.withValues(alpha: c.dark ? 0.14 : 0.10),
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
          child: Row(
            children: [
              SizedBox.square(
                dimension: 14,
                child: CircularProgressIndicator(
                  strokeWidth: 1.8,
                  color: c.honey,
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  model.connecting ? '正在连接 $name…' : '和 $name 的连接断开了，正在重连',
                  style: TextStyle(fontSize: 13, color: c.honey),
                ),
              ),
            ],
          ),
        );
      },
    );
  }
}

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
    final c = HiveColors.of(context);
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
      decoration: _askCard(c),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const StatusHex(state: AgentState.waiting, size: 18),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  '允许 ${approval.tool}？',
                  style: TextStyle(
                    fontSize: Fs.cardTitle,
                    fontWeight: FontWeight.w600,
                    color: c.text,
                  ),
                ),
              ),
            ],
          ),
          if (approval.summary.isNotEmpty) ...[
            const SizedBox(height: 8),
            Container(
              width: double.infinity,
              constraints: const BoxConstraints(maxHeight: 140),
              padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
              decoration: BoxDecoration(
                color: c.code,
                borderRadius: BorderRadius.circular(Rd.button),
              ),
              child: SingleChildScrollView(
                child: Text(
                  approval.summary,
                  style: mono(12.5, color: c.muted, height: 1.45),
                ),
              ),
            ),
          ],
          const SizedBox(height: 12),
          Row(
            children: [
              Expanded(
                child: TextButton(
                  onPressed: busy ? null : () => onDecision(denyKey),
                  child: const Text('拒绝'),
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: FilledButton(
                  onPressed: busy ? null : () => onDecision(allowKey),
                  child: const Text('允许'),
                ),
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
    final c = HiveColors.of(context);
    final questions = widget.questions;
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      constraints: BoxConstraints(
        maxHeight: MediaQuery.sizeOf(context).height * 0.5,
      ),
      decoration: _askCard(c),
      // 题目多时只滚题目，提交按钮一直在卡片底部
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Flexible(
            child: SingleChildScrollView(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (var i = 0; i < questions.length; i++) ...[
                    if (questions[i].header != null)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 2),
                        child: Text(
                          questions[i].header!,
                          style: TextStyle(color: c.accent, fontSize: 12),
                        ),
                      ),
                    Text(
                      questions[i].question,
                      style: TextStyle(
                        fontSize: Fs.body,
                        fontWeight: FontWeight.w600,
                        color: c.text,
                      ),
                    ),
                    const SizedBox(height: 8),
                    Wrap(
                      spacing: 6,
                      runSpacing: 6,
                      children: [
                        for (final option in questions[i].options)
                          FilterChip(
                            label: Text(option.label),
                            showCheckmark: false,
                            side: BorderSide.none,
                            backgroundColor: c.fill,
                            selectedColor: c.tint,
                            labelStyle: TextStyle(
                              color:
                                  (_selected[i]?.contains(option.label) ??
                                      false)
                                  ? c.accent
                                  : c.text,
                            ),
                            shape: RoundedRectangleBorder(
                              borderRadius: BorderRadius.circular(Rd.button),
                            ),
                            selected:
                                _selected[i]?.contains(option.label) ?? false,
                            onSelected: (selected) => setState(() {
                              final set = _selected.putIfAbsent(i, () => {});
                              if (questions[i].multiSelect) {
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
                    const SizedBox(height: 8),
                    TextField(
                      controller: _otherOf(i),
                      decoration: const InputDecoration(
                        hintText: '其他回答',
                        isDense: true,
                      ),
                      onChanged: (_) => setState(() {}),
                    ),
                    const SizedBox(height: 12),
                  ],
                ],
              ),
            ),
          ),
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
  const ToolRun({super.key, required this.blocks, this.initiallyOpen = false});

  final List<ChatBlock> blocks;
  final bool initiallyOpen;

  @override
  State<ToolRun> createState() => _ToolRunState();
}

class _ToolRunState extends State<ToolRun> {
  late var _open = widget.initiallyOpen;

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
            child: Container(
              padding: const EdgeInsets.fromLTRB(8, 5, 8, 5),
              decoration: BoxDecoration(
                color: HiveColors.of(context).fill,
                borderRadius: BorderRadius.circular(Rd.button),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.build_outlined, size: 14, color: muted),
                  const SizedBox(width: 6),
                  Text(
                    '工具调用 $count 次',
                    style: TextStyle(fontSize: 12.5, color: muted),
                  ),
                  const SizedBox(width: 2),
                  Icon(
                    _open
                        ? Icons.expand_less_rounded
                        : Icons.expand_more_rounded,
                    size: 16,
                    color: muted,
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
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(top: 6),
      padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
      decoration: BoxDecoration(
        color: HiveColors.of(context).code,
        borderRadius: BorderRadius.circular(Rd.button),
      ),
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
              style: mono(12, height: 1.45, color: muted),
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

  void _stickToBottom() {
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
      if (position.maxScrollExtent - position.pixels < 80) {
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
    final settings = widget.model.settings;
    final entries = foldTranscript(_window.messages)
        .where((e) => settings.showReasoning || e.message?.role != 'reasoning')
        .toList();
    final c = HiveColors.of(context);
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
          if (widget.onOpenTerminal != null)
            IconButton(
              tooltip: '终端',
              onPressed: widget.onOpenTerminal,
              icon: const Icon(Icons.terminal_rounded),
            ),
          PopupMenuButton<String>(
            tooltip: '更多',
            icon: const Icon(Icons.more_horiz_rounded),
            onSelected: (value) async {
              if (value != 'close') return;
              final nav = Navigator.of(context);
              if (await closeSession(context, widget.model, tab)) nav.pop();
            },
            itemBuilder: (_) => const [
              PopupMenuItem(value: 'close', child: Text('关闭会话')),
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
                    itemCount: entries.length + (_window.hasMore ? 1 : 0),
                    itemBuilder: (context, index) {
                      if (_window.hasMore) {
                        if (index == 0) return _earlierRow(c);
                        index--;
                      }
                      final entry = entries[index];
                      if (entry.tools) {
                        return ToolRun(
                          key: ValueKey(_toolKey(entry.blocks, index)),
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
          _composer(),
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

  Widget _composer() {
    final c = HiveColors.of(context);
    if (_ended) {
      return SafeArea(
        top: false,
        child: Container(
          width: double.infinity,
          margin: const EdgeInsets.fromLTRB(12, 4, 12, 8),
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 14),
          decoration: BoxDecoration(
            color: c.fill,
            borderRadius: BorderRadius.circular(Rd.composer),
          ),
          child: Text(
            '这个会话已经结束，记录还在。要继续请在首页新建会话，或者在电脑的历史页恢复。',
            style: TextStyle(fontSize: 13, height: 1.5, color: c.muted),
          ),
        ),
      );
    }
    final ready = !_sending && _input.text.trim().isNotEmpty;
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 4, 12, 8),
        child: Container(
          decoration: BoxDecoration(
            color: c.card,
            borderRadius: BorderRadius.circular(Rd.composer),
            border: Border.all(color: c.line),
          ),
          // 四边一样宽：单行时按钮正好在框的正中，多行时停在底部
          padding: const EdgeInsets.all(6),
          child: Row(
            // 发送按钮和输入框上下居中
            crossAxisAlignment: CrossAxisAlignment.center,
            children: [
              Expanded(
                child: TextField(
                  controller: _input,
                  minLines: 1,
                  maxLines: 6,
                  textInputAction: TextInputAction.newline,
                  onChanged: (_) => setState(() {}),
                  style: TextStyle(fontSize: Fs.body, color: c.text),
                  decoration: InputDecoration(
                    hintText: '回复 ${chatWho('assistant', tab.agent)}',
                    filled: false,
                    border: InputBorder.none,
                    enabledBorder: InputBorder.none,
                    focusedBorder: InputBorder.none,
                    isDense: true,
                    // 单行时和 34 高的发送按钮一样高
                    contentPadding: const EdgeInsets.fromLTRB(10, 8, 4, 8),
                  ),
                ),
              ),
              AnimatedContainer(
                duration: const Duration(milliseconds: 150),
                width: 34,
                height: 34,
                decoration: BoxDecoration(
                  color: ready ? c.accent : c.fill,
                  shape: BoxShape.circle,
                ),
                child: IconButton(
                  padding: EdgeInsets.zero,
                  tooltip: '发送',
                  onPressed: ready
                      ? () => _send(_input.text, enter: true)
                      : null,
                  icon: Icon(
                    Icons.arrow_upward_rounded,
                    size: 20,
                    color: ready ? Colors.white : c.faint,
                  ),
                ),
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
