import 'dart:async';
import 'dart:typed_data';

import 'package:file_selector/file_selector.dart' show openFile;
import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:scrollview_observer/scrollview_observer.dart';

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
import 'anchor.dart';
import '../files/files.dart';
import '../workspace/session_actions.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'bubbles.dart';
import 'cards.dart';
import 'composer.dart';
import 'draft.dart';
import 'echo.dart';
import 'session_bar.dart';
import 'media.dart';
import 'tools.dart';

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

  final PosseAppModel model;
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
  late final ListObserverController _observer = ListObserverController(
    controller: _scroll,
  )..cacheJumpIndexOffset = false;
  late final ChatScrollObserver _chatObserver = ChatScrollObserver(_observer)
    ..fixedPositionOffset = 80
    ..toRebuildScrollViewCallback = () {
      if (mounted) setState(() {});
    };
  SessionTab? _tab;
  RpcStream? _chat;
  RpcStream? _tabs;
  String? _chatSubId;
  PhoneRpc? _bound;
  var _perm = const LivePermission();
  var _sending = false;
  var _loadingEarlier = false;

  /// 正在上传的图片：第几张、共几张、这一张传了多少。没在传时为空。
  (int, int, double)? _upload;

  /// 选好、还没发出去的图片。发出去时按顺序存到电脑的临时目录，再把路径写进终端。
  final _pending = <Uint8List>[];

  /// 已经传到电脑上的图片和路径。发送失败后重发不再传一遍。
  final _uploaded = <Uint8List, String>{};

  /// 发出去、会话文件里还没出现的话。
  final _echoes = LocalEchoes();

  /// 人在看更早的消息时，底部又来了新内容。不用观察器的位置回调：它推迟到帧后，
  /// 每次布局都发，先后次序和贴底的跳转对不上，按钮会闪或在底部亮着。
  var _newBelow = false;

  /// 自己刚发了话：之后的新内容都贴底跟着，直到人自己拖动列表。
  var _stick = false;

  /// 电脑推来的 agent 状态行（终端底部转圈那一行），没有时为空。
  String? _status;

  /// 电脑推来的、agent 正在写的那段回复（posse.draft.v1），没有时为空。
  String? _draft;

  /// 这个会话的终端已经不在了：输入框停用，顶上说一声。
  var _ended = false;
  var _gone = false;
  var _openGen = 0;
  Future<void> _gate = Future<void>.value();
  String? _notice;

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

  SessionTab get tab => _tab ?? widget.tab;

  PhoneRpc? get _rpc => widget.model.rpc;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _scroll.addListener(_onScroll);
    _scheduleOpen();
  }

  /// 倒序列表的偏移 0 是底部。靠近顶部就读更早的消息；回到底部就收起「新消息」。
  void _onScroll() {
    if (!_scroll.hasClients) return;
    final position = _scroll.position;
    if (_newBelow && position.pixels <= 80) {
      setState(() => _newBelow = false);
    }
    if (nearEarliest(position)) unawaited(_loadEarlier());
  }

  void _onModel() {
    if (!mounted) return;
    // 断线时草稿不再更新，撤掉；连上后电脑会重新推
    if (widget.model.rpc == null) _setDraft(null);
    if (widget.model.rpc != _bound) _scheduleOpen();
  }

  void _scheduleOpen() {
    _gate = _gate.then((_) => _open()).catchError((Object _) {});
  }

  @override
  void dispose() {
    _noticeTimer?.cancel();
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
    _draft = null;
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
      // 订阅是单订阅流，等动画的这段时间来的帧会留着
      if (!mounted) return;
      await routeSettled(context);
      if (gen != _openGen || _gone) return;
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
          if (next != _status) {
            final before = _tail();
            _status = next;
            _anchor(before);
            setState(() {});
          }
          return;
        }
        if (frame['type'] == 'draft') {
          final text = frame['text'];
          _setDraft(text is String && text.isNotEmpty ? text : null);
          return;
        }
        if (frame['type'] == 'end') {
          _setDraft(null);
          return;
        }
        final before = _tail();
        // 正式的回复到了，草稿让位；电脑那边也会跟着推空草稿，这里不等它
        if (frameHasReply(frame)) _draft = null;
        // 人往上翻着看时不裁窗口：裁掉的正是在看的那段，展开的工具组也会跟着收起。回到底部的下一帧再裁
        final atBottom = !_scroll.hasClients || _scroll.position.pixels <= 80;
        _window.applyFrame(frame, limit: atBottom ? 40 : 0);
        _echoes.reconcile(_window.messages);
        _anchor(before);
        setState(() {});
        _loadEarlierIfAtTop();
      }, onError: (Object _) {});
      tabs.frames.listen((frame) {
        if (!mounted || gen != _openGen) return;
        final nextTabs = tabsFromFrame(tab.worktreeId, frame);
        if (nextTabs == null) return;
        final before = _tail();
        var changed = false;
        final alive = nextTabs.any((t) => t.terminalId == tab.terminalId);
        if (alive == _ended) {
          _ended = !alive;
          changed = true;
        }
        for (final next in nextTabs) {
          if (next.terminalId != tab.terminalId) continue;
          _tab = next;
          _perm = _perm.observe(waiting: next.state == 'waiting');
          changed = true;
        }
        if (!changed) return;
        _anchor(before);
        setState(() {});
      }, onError: (Object _) {});
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

  /// 倒序列表打开就停在最新一条。自己刚发的话无条件回到底部。
  void _jumpToLatest() {
    _newBelow = false;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (_scroll.hasClients && _scroll.position.pixels != 0) {
        _scroll.jumpTo(0);
      }
      if (_newBelow) setState(() => _newBelow = false);
    });
  }

  /// 内容不满一屏时没有滚动事件，看完一页如果还贴着顶部就继续往前读。
  void _loadEarlierIfAtTop() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients || _loadingEarlier) return;
      if (nearEarliest(_scroll.position)) unawaited(_loadEarlier());
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
      // 更早的消息加在倒序列表的尾部（视觉顶部）。底部偏移是 0，已有下标不变，不用挪像素。
      final beforeCount = _window.messages.length;
      final tail = _tail();
      _window.applyOlderPage(page);
      _anchor(tail);
      setState(() {});
      if (_window.messages.length > beforeCount) _loadEarlierIfAtTop();
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
    _flash(note.isEmpty ? null : note);
    setState(() {
      if (outcome == SendOutcome.accepted && enter && text == _input.text) {
        _input.clear();
      }
    });
    return outcome;
  }

  void _addImages(List<Uint8List> images) {
    if (!mounted || images.isEmpty) return;
    final room = maxChatImages - _pending.length;
    setState(() => _pending.addAll(images.take(room)));
    if (images.length > room) _flash('一次最多 $maxChatImages 张');
  }

  /// 视频和「文件」里的选了就传到电脑上，路径一个一行接在输入框后面，和图片的路径一样交给 agent 去读。
  /// 照片不走这里：先在输入框上面预览，发送时才传。
  Future<void> _attachFiles(List<XFile> files) async {
    final rpc = _rpc;
    if (files.isEmpty || rpc == null) return;
    final paths = <String>[];
    try {
      for (final (i, file) in files.indexed) {
        if (mounted) setState(() => _upload = (i, files.length, 0));
        paths.add(
          await uploadFile(
            rpc,
            file,
            onProgress: (done) {
              if (mounted) setState(() => _upload = (i, files.length, done));
            },
          ),
        );
      }
    } finally {
      if (mounted) setState(() => _upload = null);
    }
    if (!mounted) return;
    final text = _input.text;
    final lines = paths.map(shellPath).join('\n');
    _input.text = text.isEmpty || text.endsWith('\n')
        ? '$text$lines'
        : '$text\n$lines';
    _input.selection = TextSelection.collapsed(offset: _input.text.length);
    setState(() {});
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
      _flash('剪贴板里没有文字或图片');
    } catch (error) {
      if (mounted) _flash(friendlyError(error));
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
      final ask = await askImageSource(context);
      if (ask == null || !mounted) return;
      switch (ask) {
        case ImageAsk.gallery:
          final picked = await pickGalleryMedia();
          final images = <Uint8List>[];
          final others = <XFile>[];
          for (final file in picked) {
            if (isImageFile(file.name)) {
              images.add(await file.readAsBytes());
            } else {
              others.add(file);
            }
          }
          _addImages(images);
          await _attachFiles(others);
        case ImageAsk.camera:
          final one = await pickOneImage(ImageSource.camera);
          if (one != null) _addImages([one]);
        case ImageAsk.clipboard:
          await _pasteClipboard();
        case ImageAsk.files:
          final file = await openFile();
          if (file != null) await _attachFiles([file]);
      }
    } catch (error) {
      if (mounted) _flash(friendlyError(error));
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
    // 自己发的话无条件回到底部，不设保持位置：它晚一帧生效，会把刚贴底的画面拉回去
    final echo = caption.trimLeft().startsWith('/') && images.isEmpty
        ? null
        : _echoes.add(caption.trim(), images: images.length);
    _stick = true;
    setState(() {
      _sending = true;
      // 输入框马上清空，话先画在对话末尾；没发出去再放回来
      _input.clear();
      if (images.isNotEmpty) _upload = (0, images.length, 0);
    });
    _jumpToLatest();
    var accepted = false;
    try {
      var text = caption;
      if (images.isNotEmpty) {
        final paths = <String>[];
        for (final (i, image) in images.indexed) {
          if (mounted) setState(() => _upload = (i, images.length, 0));
          paths.add(
            _uploaded[image] ??= await uploadImage(
              current,
              image,
              onProgress: (done) {
                if (mounted) setState(() => _upload = (i, images.length, done));
              },
            ),
          );
        }
        if (mounted) setState(() => _upload = null);
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
      if (mounted) _flash(friendlyError(error));
    } finally {
      if (mounted) {
        final before = _tail();
        _sending = false;
        _upload = null;
        if (!accepted) {
          if (echo != null) _echoes.remove(echo);
          if (_input.text.isEmpty) _input.text = caption;
        }
        _anchor(before);
        setState(() {});
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
    final entryKeys = transcriptKeys(entries);
    final c = PosseColors.of(context);
    final echoes = _echoes.items;
    final working = _working;
    // 倒序列表底部一加行，所有下标都挪一格。按身份认行，展开着的工具组这类状态才不会错配到别的行上
    final draft = _draft;
    final rowKeys = <String>[
      if (working) 'working',
      if (draft != null) 'draft',
      for (var i = echoes.length - 1; i >= 0; i--)
        'echo:${identityHashCode(echoes[i])}',
      for (var i = entries.length - 1; i >= 0; i--) entryKeys[i],
      if (_window.hasMore) 'earlier',
    ];
    final rowIndex = {for (var i = 0; i < rowKeys.length; i++) rowKeys[i]: i};
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
          IconButton(
            tooltip: '更多',
            icon: const Icon(Icons.more_horiz_rounded),
            onPressed: () async {
              final nav = Navigator.of(context);
              final action = await showSessionMore(
                context,
                tab: tab,
                switchLabel: widget.onOpenTerminal == null ? null : '切换到终端',
                switchIcon: Icons.terminal_rounded,
                media: hasMedia(tab),
                tools: hasTools(tab),
              );
              if (!mounted) return;
              if (action == 'media') {
                nav.push(
                  MaterialPageRoute<void>(
                    builder: (_) => MediaScreen(model: widget.model, tab: tab),
                  ),
                );
              }
              if (action == 'tools') {
                nav.push(
                  MaterialPageRoute<void>(
                    builder: (_) => ToolsScreen(model: widget.model, tab: tab),
                  ),
                );
              }
              if (action == 'switch') widget.onOpenTerminal?.call();
              if (action == 'close' &&
                  await closeSession(this.context, widget.model, tab)) {
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
          if (!tab.eligible)
            const Expanded(child: Center(child: Text('这个会话没有原生存档')))
          else ...[
            if (_window.pending && _window.messages.isEmpty)
              const LinearProgressIndicator(minHeight: 2),
            if (_window.error != null) NoticeBar(_window.error!),
            Expanded(
              child: Stack(
                children: [
                  Positioned.fill(
                    child: NotificationListener<ScrollStartNotification>(
                      onNotification: (n) {
                        if (n.dragDetails != null) _stick = false;
                        return false;
                      },
                      child: SelectionArea(
                        child: Scrollbar(
                          controller: _scroll,
                          child: ListViewObserver(
                            controller: _observer,
                            child: ListView.builder(
                              controller: _scroll,
                              reverse: true,
                              physics: _listPhysics(context),
                              padding: const EdgeInsets.fromLTRB(0, 8, 0, 24),
                              // 下标 0 是视觉底部。还有更早的消息时，列表最上面多一行。
                              // 行按身份认（rowKeys）：展开着的工具组不会在别的行插进来时收起
                              itemCount: rowKeys.length,
                              findChildIndexCallback: (key) =>
                                  key is ValueKey<String>
                                  ? rowIndex[key.value]
                                  : null,
                              itemBuilder: (context, index) => KeyedSubtree(
                                key: ValueKey(rowKeys[index]),
                                child: _row(
                                  c,
                                  index,
                                  working: working,
                                  draft: draft,
                                  echoes: echoes,
                                  entries: entries,
                                  settings: settings,
                                ),
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
                  // 提示、上传进度、「新消息」都浮在列表底部，不挤动列表
                  if (_notice != null || _upload != null || _newBelow)
                    Positioned(
                      left: 0,
                      right: 0,
                      bottom: 12,
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          if (_notice != null)
                            NoticeBar(
                              _notice!,
                              floating: true,
                              onClose: () => _flash(null),
                            ),
                          if (_notice != null && (_upload != null || _newBelow))
                            const SizedBox(height: 8),
                          if (_upload case (final i, final n, final done))
                            UploadBar(index: i, count: n, done: done)
                          else if (_newBelow)
                            Center(child: _newBelowButton(c)),
                        ],
                      ),
                    ),
                ],
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

  Widget _row(
    PosseColors c,
    int index, {
    required bool working,
    required String? draft,
    required List<LocalEcho> echoes,
    required List<TranscriptEntry> entries,
    required AppSettings settings,
  }) {
    var at = index;
    if (working) {
      if (at == 0) {
        return WorkingRow(status: _status, tool: tab.toolName);
      }
      at--;
    }
    if (draft != null) {
      if (at == 0) return DraftBubble(text: draft, agent: tab.agent);
      at--;
    }
    if (at < echoes.length) {
      return EchoBubble(echo: echoes[echoes.length - 1 - at]);
    }
    at -= echoes.length;
    if (at < entries.length) {
      final entry = entries[entries.length - 1 - at];
      if (entry.tools) {
        return ToolRun(
          blocks: entry.blocks,
          initiallyOpen: settings.expandTools,
        );
      }
      return MessageBubble(
        message: entry.message!,
        blocks: entry.blocks,
        agent: tab.agent,
      );
    }
    return EarlierRow(loading: _loadingEarlier, onLoad: _loadEarlier);
  }

  ScrollPhysics _listPhysics(BuildContext context) {
    final platform = Theme.of(context).platform;
    if (platform == TargetPlatform.iOS || platform == TargetPlatform.macOS) {
      return ChatObserverBouncingScrollPhysics(observer: _chatObserver);
    }
    return ChatObserverClampingScrollPhysics(observer: _chatObserver);
  }

  /// agent 在干活时对话最后一行转着星号，看得出它还在；停下、断线或会话结束就不画。
  bool get _working {
    return !_ended &&
        _rpc != null &&
        (agentStateOf(tab.state) == AgentState.working ||
            (_status?.isNotEmpty ?? false));
  }

  /// 草稿变了：断线、撤掉都传 null。
  void _setDraft(String? next) {
    if (!mounted || next == _draft) return;
    final before = _tail();
    _draft = next;
    _anchor(before);
    setState(() {});
  }

  /// 倒序列表的下标顺序：下标 0 是视觉底部。不含「更早的消息」那一行，它只加在尾部。
  ChatTail _tail() {
    final settings = widget.model.settings;
    final entries = foldTranscript(_window.messages)
        .where((e) => settings.showReasoning || e.message?.role != 'reasoning')
        .toList();
    final echoes = _echoes.items;
    final keys = <String>[];
    final sizes = <int>[];
    if (_working) {
      keys.add('working');
      sizes.add(_status?.length ?? 0);
    }
    if (_draft case final draft?) {
      keys.add('draft');
      sizes.add(draft.length);
    }
    for (var i = echoes.length - 1; i >= 0; i--) {
      final echo = echoes[i];
      keys.add('echo:${identityHashCode(echo)}');
      sizes.add(echo.text.length + echo.images);
    }
    final ids = transcriptKeys(entries);
    for (var i = entries.length - 1; i >= 0; i--) {
      keys.add(ids[i]);
      sizes.add(entrySize(entries[i]));
    }
    return ChatTail(keys, sizes);
  }

  /// 插入、删除或变高之前记下参照行，布局后按它修正偏移。纯往顶部追加历史时不修正。
  void _anchor(ChatTail before) {
    if (!_scroll.hasClients) return;
    if (_stick) {
      _jumpToLatest();
      return;
    }
    final first = _observer.observeFirstItem()?.index ?? 0;
    final plan = anchorPlan(before, _tail(), from: first);
    if (plan == null) return;
    if (_scroll.position.pixels > _chatObserver.fixedPositionOffset) {
      _newBelow = true;
    }
    _chatObserver.standby(
      mode: ChatScrollObserverHandleMode.specified,
      refIndexType: ChatScrollObserverRefIndexType.itemIndex,
      refItemIndex: plan.before,
      refItemIndexAfterUpdate: plan.after,
    );
  }

  Widget _newBelowButton(PosseColors c) {
    return TextButton(
      style: TextButton.styleFrom(
        minimumSize: const Size(0, 32),
        padding: const EdgeInsets.symmetric(horizontal: 12),
        backgroundColor: c.card,
        foregroundColor: c.accent,
        textStyle: const TextStyle(fontSize: 13, fontWeight: FontWeight.w500),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(16),
          side: BorderSide(color: c.line),
        ),
      ),
      onPressed: () {
        if (_scroll.hasClients) _scroll.jumpTo(0);
        setState(() => _newBelow = false);
      },
      child: const Text('新消息'),
    );
  }
}
