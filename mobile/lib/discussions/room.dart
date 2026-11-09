import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:file_selector/file_selector.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:scrollview_observer/scrollview_observer.dart';

import '../app_state.dart';
import '../chat/bubbles.dart';
import '../chat/image_pick.dart';
import '../chat/markdown_view.dart';
import '../files/save.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'api.dart';
import 'forms.dart';
import 'models.dart';
import 'profile.dart';

part 'room_composer.dart';
part 'room_details.dart';
part 'room_message.dart';

class DiscussionRoom extends StatefulWidget {
  const DiscussionRoom({
    super.key,
    required this.api,
    required this.room,
    required this.self,
  });
  final DiscussionApi api;
  final String room, self;
  @override
  State<DiscussionRoom> createState() => _DiscussionRoomState();
}

class _DiscussionRoomState extends State<DiscussionRoom>
    with WidgetsBindingObserver {
  late final state = DiscussionState(widget.api.call, widget.room);
  late final thumbs = DiscThumbs(widget.api);
  late final storage = DiscussionStorage(
    widget.api.computer ?? '',
    widget.room,
  );
  final input = TextEditingController();
  final scroll = ScrollController();
  late final observer = ListObserverController(controller: scroll)
    ..cacheJumpIndexOffset = false;
  Timer? timer, saveTimer;
  bool foreground = true, sending = false, uploading = false, restored = false;
  double progress = 0;
  String quote = '', target = '', mention = '', clientId = '';
  int read = 0;
  List<DiscJson> attachments = [];
  final localFiles = <String, XFile>{};
  DiscJson? pending;
  Object? error;

  /// 这台电脑的名字和头像（profile.get）；旧电脑没有这个接口时为空。
  DiscJson ownProfile = {};
  bool get writable =>
      state.discussion.isNotEmpty &&
      widget.api.connected &&
      state.discussion['status'] != 'archived' &&
      (state.discussion['mirror'] != true ||
          state.collab['state'] == 'active' &&
              state.collab['connected'] == true);
  bool get atBottom => !scroll.hasClients || scroll.position.extentAfter < 48;
  bool wasAtBottom = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    state.addListener(changed);
    scroll.addListener(scrolled);
    input.addListener(scheduleSave);
    unawaited(start());
    unawaited(loadProfile());
    timer = Timer.periodic(const Duration(seconds: 2), (_) {
      if (foreground && ModalRoute.of(context)?.isCurrent == true) {
        unawaited(poll());
      }
    });
  }

  Future<void> start() async {
    final saved = await storage.read();
    if (!mounted) return;
    setState(() {
      input.text = discText(saved, 'body');
      quote = discText(saved, 'quote');
      target = discText(saved, 'target');
      mention = discText(saved, 'mention');
      clientId = discText(saved, 'clientId');
      read = discInt(saved, 'read');
      attachments = discList(saved['attachments']);
      if (saved['pending'] is Map) pending = discMap(saved['pending']);
      restored = true;
    });
    await state.page();
    await bottom();
  }

  Future<void> loadProfile() async {
    try {
      final r = await widget.api.call('profile.get', {});
      if (mounted) setState(() => ownProfile = r);
    } catch (_) {
      // 旧电脑：头像画首字，没有资料入口
    }
  }

  Future<void> editProfile() async {
    if (!await openProfile(context, widget.api) || !mounted) return;
    await loadProfile();
    await state.poll();
  }

  void changed() {
    if (mounted) setState(() {});
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState s) {
    foreground = s == AppLifecycleState.resumed;
    if (foreground) {
      unawaited(poll());
    } else {
      unawaited(save());
    }
  }

  @override
  void dispose() {
    timer?.cancel();
    saveTimer?.cancel();
    unawaited(save());
    WidgetsBinding.instance.removeObserver(this);
    input.removeListener(scheduleSave);
    input.dispose();
    scroll.dispose();
    state.removeListener(changed);
    state.dispose();
    super.dispose();
  }

  void scheduleSave() {
    if (!restored) return;
    saveTimer?.cancel();
    saveTimer = Timer(const Duration(milliseconds: 300), () {
      unawaited(save());
    });
  }

  Future<void> save() => storage.write({
    'body': input.text,
    'quote': quote,
    'target': target,
    'mention': mention,
    'clientId': clientId,
    'read': read,
    'attachments': attachments,
    if (pending != null) 'pending': pending,
  });
  void scrolled() {
    if (mounted && wasAtBottom != atBottom) {
      setState(() => wasAtBottom = atBottom);
    }
    if (atBottom &&
        state.latest &&
        foreground &&
        ModalRoute.of(context)?.isCurrent == true &&
        read < state.last) {
      read = state.last;
      scheduleSave();
    }
  }

  Future<void> poll() async {
    if (!widget.api.connected) {
      if (mounted) setState(() {});
      return;
    }
    final follow = atBottom;
    await state.poll();
    if (!mounted) return;
    if (follow && state.latest) await bottom();
  }

  Future<void> bottom() async {
    if (!mounted) return;
    await WidgetsBinding.instance.endOfFrame;
    if (!mounted || !scroll.hasClients) return;
    if (state.messages.isNotEmpty) {
      final index = state.messages.length - 1 + (state.more ? 1 : 0);
      // 尾项已布局时直接对齐底部，避免轮询先跳到消息顶部再跳回来。
      if (observer.observeItem(index: index) == null) {
        await observer.jumpTo(index: index);
      }
    }
    if (!mounted || !scroll.hasClients) return;
    if (scroll.offset != scroll.position.maxScrollExtent) {
      scroll.jumpTo(scroll.position.maxScrollExtent);
    }
    scrolled();
  }

  Future<void> latest() async {
    await state.page();
    await bottom();
  }

  Future<void> locate(String id) async {
    await state.page(message: id);
    if (!mounted) return;
    await WidgetsBinding.instance.endOfFrame;
    if (!mounted) return;
    final index = state.messages.indexWhere((m) => m['id'] == id);
    if (index >= 0 && scroll.hasClients) await observer.jumpTo(index: index);
  }

  Future<void> earlier() async {
    final anchor = observer.observeFirstItem()?.index ?? 0;
    final id = state.messages.isEmpty
        ? ''
        : discText(
            state.messages[anchor.clamp(0, state.messages.length - 1)],
            'id',
          );
    await state.page(before: state.first);
    if (!mounted) return;
    await WidgetsBinding.instance.endOfFrame;
    if (!mounted) return;
    final index = state.messages.indexWhere((m) => m['id'] == id);
    if (index >= 0 && scroll.hasClients) await observer.jumpTo(index: index);
  }

  Future<void> send() async {
    if (sending || !writable || uploading) return;
    if (pending == null && input.text.trim().isEmpty && attachments.isEmpty) {
      return;
    }
    setState(() {
      sending = true;
      error = null;
    });
    clientId = clientId.isEmpty ? discRequestId() : clientId;
    pending ??= {
      'discussion': widget.room,
      'body': input.text,
      'quote': quote,
      'target': target,
      'mention': mention,
      'clientId': clientId,
      'attachments': attachments
          .map((a) => {'staged': a['staged'], 'name': a['name']})
          .toList(),
    };
    await save();
    try {
      await widget.api.call('post', pending!);
      if (!mounted) return;
      setState(() {
        input.clear();
        quote = '';
        target = '';
        mention = '';
        attachments = [];
        localFiles.clear();
        clientId = '';
        pending = null;
      });
      await save();
      await latest();
    } catch (e) {
      if (!mounted) return;
      if (e is RpcException && e.code == 'upload_expired') {
        // 只更换过期的上传凭据，消息身份及正文保持不变。
        try {
          final fresh = <DiscJson>[];
          for (final a in attachments) {
            var file = localFiles[discText(a, 'staged')];
            if (file == null || !await File(file.path).exists()) {
              if (!mounted) return;
              final ok = await showPosseConfirm(
                context,
                title: discText(a, 'name'),
                body: context.l10n.discDraftFilesExpired,
                confirm: context.l10n.discAddFile,
              );
              if (!ok || !mounted) return;
              file = await openFile();
            }
            if (file == null) return;
            if (await file.length() != discInt(a, 'size') ||
                (await sha256.bind(file.openRead()).first).toString() !=
                    a['sha']) {
              throw RpcException('discussion_reselect', '');
            }
            final uploaded = await widget.api.upload(file, (_) {});
            fresh.add(uploaded);
            localFiles[discText(uploaded, 'staged')] = file;
          }
          if (!mounted) return;
          setState(() {
            attachments = fresh;
            pending!['attachments'] = fresh
                .map((a) => {'staged': a['staged'], 'name': a['name']})
                .toList();
          });
          await save();
        } catch (retryError) {
          if (mounted) setState(() => error = retryError);
        }
      } else {
        setState(() => error = e);
      }
    } finally {
      if (mounted) setState(() => sending = false);
    }
  }

  Future<void> attach({bool photo = false}) async {
    if (attachments.length >= 10) {
      setState(() => error = RpcException('discussion_file_limit', ''));
      return;
    }
    try {
      final XFile? file;
      if (photo) {
        final files = await pickGalleryMedia(limit: 1);
        file = files.isEmpty ? null : files.first;
      } else {
        final ask = await askFileSource(context);
        file = ask == null ? null : await pickAskedFile(ask);
      }
      if (file == null || !mounted) return;
      setState(() {
        uploading = true;
        progress = 0;
      });
      final result = await widget.api.upload(file, (v) {
        if (mounted) setState(() => progress = v);
      });
      if (!mounted) return;
      setState(() {
        attachments.add(result);
        localFiles[discText(result, 'staged')] = file!;
      });
      scheduleSave();
    } catch (e) {
      if (mounted) setState(() => error = e);
    } finally {
      if (mounted) setState(() => uploading = false);
    }
  }

  Future<void> removeAttachment(DiscJson a) async {
    setState(() => attachments.remove(a));
    scheduleSave();
    try {
      await widget.api.call('upload.abort', {'staged': a['staged']});
    } catch (e) {
      if (mounted) setState(() => error = e);
    }
  }

  Future<void> download(DiscJson m, DiscJson a) async {
    Directory? dir;
    try {
      final bytes = await widget.api.download(
        widget.room,
        discText(m, 'id'),
        discText(a, 'id'),
      );
      if (!mounted) return;
      if (discText(a, 'type').startsWith('image/')) {
        final saveImage = await showPosseDialog<bool>(
          context: context,
          builder: (_) => AlertDialog(
            title: Text(
              discText(a, 'name'),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
            content: InteractiveViewer(
              child: Image.memory(
                bytes,
                errorBuilder: (_, _, _) => Text(context.l10n.discDownloadError),
              ),
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: Text(context.l10n.discClose),
              ),
              TextButton(
                onPressed: () => Navigator.pop(context, true),
                child: Text(context.l10n.discSave),
              ),
            ],
          ),
        );
        if (!mounted || saveImage != true) return;
      }
      dir = await Directory.systemTemp.createTemp('posse-disc-');
      final file = File('${dir.path}/attachment');
      await file.writeAsBytes(bytes);
      if (mounted) await saveToPhone(context, file, discText(a, 'name'));
    } catch (e) {
      if (mounted) setState(() => error = e);
    } finally {
      await dir?.delete(recursive: true);
    }
  }

  Future<void> report(DiscJson message) async {
    try {
      final bytes = await widget.api.download(
        widget.room,
        discText(message, 'id'),
        '',
        report: true,
      );
      if (!mounted) return;
      await showPosseDialog<void>(
        context: context,
        builder: (dialog) => AlertDialog(
          title: Text(context.l10n.discReport),
          content: SizedBox(
            width: MediaQuery.sizeOf(context).width,
            height: MediaQuery.sizeOf(context).height * 0.6,
            child: SingleChildScrollView(
              child: SelectionArea(
                child: MarkdownView(utf8.decode(bytes, allowMalformed: true)),
              ),
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(dialog),
              child: Text(context.l10n.discClose),
            ),
          ],
        ),
      );
    } catch (e) {
      if (mounted) setState(() => error = e);
    }
  }

  Future<void> action(
    String method,
    DiscJson params, {
    String? title,
    String? body,
  }) async {
    if (title != null &&
        !await showPosseConfirm(
          context,
          title: title,
          body: body,
          // 确认按钮写这个操作本身（归档、分享、移除……），不写通用的「保存」
          confirm: title,
          tone: ConfirmTone.warning,
        )) {
      return;
    }
    try {
      await widget.api.call(method, params);
      await state.poll();
    } catch (e) {
      if (!mounted) return;
      // 操作没成（比如要停的那轮已经结束）是一次性的提示，过一会儿自己消失
      setState(() => error = e);
      Timer(noticeLife, () {
        if (mounted && identical(error, e)) setState(() => error = null);
      });
    }
  }

  Future<void> answer(DiscJson m) async {
    final l = context.l10n;
    await discussionForm(
      context,
      widget.api,
      l.discAnswer,
      'answer',
      {'question': m['question']},
      {'body': l.discAnswer},
    );
    await state.poll();
  }

  void worker(DiscJson row) {
    final worktree = discText(row, 'workspaceId'),
        terminal = discText(row, 'worker');
    if (worktree.isEmpty || terminal.isEmpty || !discMine(row, widget.self)) {
      return;
    }
    openSession(
      context,
      widget.api.model,
      SessionTab(
        worktreeId: worktree,
        terminalId: terminal,
        title: discText(row, 'name'),
        agent: discText(row, 'agent'),
      ),
    );
  }

  Future<void> member([DiscJson? row]) async {
    final l = context.l10n;
    final add = row == null;
    await discussionForm(
      context,
      widget.api,
      add ? l.discAddMember : discText(row, 'name'),
      add ? 'member.add' : 'member.update',
      {
        'discussion': widget.room,
        ...?row,
        if (row != null) 'member': row['id'],
        'workspace':
            row?['workspaceId'] ?? state.discussion['workspaceId'] ?? '',
      },
      {
        'name': l.discName,
        'role': l.discRole,
        'model': l.discModel,
        'effort': l.discEffort,
        'worktree': l.discNewWorktree,
        'base': l.discBase,
      },
      workspace: true,
      agent: true,
      applyNow: !add,
      project: state.discussion['mirror'] == true
          ? null
          : discText(state.discussion, 'projectId'),
      people: state.collab.isEmpty
          ? null
          : activePeople.where((p) => p['fp'] != selfFp).toList(),
    );
    await state.poll();
  }

  Future<void> search() async {
    final l = context.l10n;
    final result = await discussionForm(
      context,
      widget.api,
      l.discSearch,
      'search',
      {'discussion': widget.room},
      {'query': l.discSearch},
    );
    if (!mounted || result == null) return;
    final selected = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (sheet) => SafeArea(
        child: ListView(
          children: [
            for (final m in discList(result['value']))
              ListTile(
                title: Text(
                  discText(m, 'body'),
                  maxLines: 4,
                  overflow: TextOverflow.ellipsis,
                ),
                subtitle: Text(discAuthorName(m, state.collab)),
                onTap: () => Navigator.pop(sheet, discText(m, 'id')),
              ),
          ],
        ),
      ),
    );
    if (selected != null) await locate(selected);
  }

  String status(String value) {
    final l = context.l10n;
    return switch (value) {
      'queued' => l.discStateQueued,
      'held' => l.discStateHeld,
      'blocked' => l.discStateBlocked,
      'starting' => l.discStateStarting,
      'running' => l.discStateRunning,
      'waiting' => l.discStateWaiting,
      'done' => l.discStateDone,
      'failed' => l.discStateFailed,
      'interrupted' => l.discStateInterrupted,
      'stopped' => l.discStateStopped,
      'cancelled' => l.discStateCancelled,
      'claimed' => l.discStateClaimed,
      'revoked' => l.discStateRevoked,
      _ => value,
    };
  }

  /// 给 part 文件里的扩展用：setState 只能在 State 里调。
  void update(VoidCallback fn) {
    if (mounted) setState(fn);
  }

  String get unavailable {
    final l = context.l10n;
    return state.discussion['status'] == 'archived'
        ? l.discReadOnly
        : state.collab['state'] == 'pending'
        ? l.discPending
        : ['removed', 'left', 'rejected'].contains(state.collab['state'])
        ? l.discRevoked
        : l.discOffline;
  }

  Future<void> editDraft() async {
    final l = context.l10n;
    if (!await showPosseConfirm(
          context,
          title: l.discEditDraft,
          body: l.discEditDraftWarning,
          confirm: l.discEditDraft,
          tone: ConfirmTone.warning,
        ) ||
        !mounted) {
      return;
    }
    setState(() {
      pending = null;
      clientId = '';
    });
    scheduleSave();
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final shownError = error ?? state.error;
    final title = discText(state.discussion, 'title');
    final count =
        state.messages.length +
        (state.more ? 1 : 0) +
        (state.moreAfter ? 1 : 0);
    return Scaffold(
      appBar: AppBar(
        title: Text(title.isEmpty ? l.discTitle : title),
        actions: [
          IconButton(
            tooltip: l.discSearch,
            onPressed: search,
            icon: const Icon(Icons.search_rounded),
          ),
          IconButton(
            tooltip: l.discDetails,
            onPressed: details,
            icon: const Icon(Icons.more_horiz_rounded),
          ),
        ],
      ),
      body: SideSafe(
        child: Column(
          children: [
            if (shownError != null)
              NoticeBar(
                friendlyError(shownError, l),
                onClose: () {
                  setState(() => error = null);
                  state.dismissError();
                },
              ),
            Expanded(
              child: Stack(
                children: [
                  if (state.messages.isEmpty)
                    state.loading
                        ? const Center(child: HexSpinner(size: Ic.hero))
                        : EmptyState(
                            icon: Icons.forum_rounded,
                            text: l.discNoMessages,
                          )
                  else
                    // 消息高度在图片、Markdown 排好后才定下来，不滚动时也要重算是否在底部
                    NotificationListener<ScrollMetricsNotification>(
                      onNotification: (_) {
                        scrolled();
                        return false;
                      },
                      child: ListViewObserver(
                        controller: observer,
                        child: ListView.builder(
                          controller: scroll,
                          padding: const EdgeInsets.symmetric(vertical: 8),
                          itemCount: count,
                          itemBuilder: (_, i) {
                            if (state.more && i == 0) {
                              return EarlierRow(
                                loading: state.loading,
                                onLoad: earlier,
                              );
                            }
                            final j = i - (state.more ? 1 : 0);
                            if (j >= state.messages.length) {
                              return Center(
                                child: TextButton(
                                  onPressed: () =>
                                      state.page(after: state.last),
                                  child: Text(l.discLater),
                                ),
                              );
                            }
                            return message(state.messages[j]);
                          },
                        ),
                      ),
                    ),
                  if (!atBottom && state.messages.isNotEmpty)
                    Positioned(
                      right: 16,
                      bottom: 12,
                      child: Material(
                        color: c.raised,
                        shape: const CircleBorder(),
                        elevation: 2,
                        shadowColor: c.shadow,
                        child: IconButton(
                          tooltip: l.discLatest,
                          onPressed: state.moreAfter ? latest : bottom,
                          icon: Icon(
                            Icons.arrow_downward_rounded,
                            size: Ic.medium,
                            color: c.muted,
                          ),
                        ),
                      ),
                    ),
                  if (uploading)
                    Positioned(
                      left: 0,
                      right: 0,
                      bottom: 12,
                      child: UploadBar(
                        index: 0,
                        count: 1,
                        done: progress,
                        files: true,
                      ),
                    ),
                ],
              ),
            ),
            composer(),
          ],
        ),
      ),
    );
  }
}
