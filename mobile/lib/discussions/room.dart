import 'dart:async';
import 'dart:io';
import 'dart:convert';

import 'package:file_selector/file_selector.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:scrollview_observer/scrollview_observer.dart';

import '../app_state.dart';
import '../chat/image_pick.dart';
import '../chat/markdown_view.dart';
import '../files/save.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'api.dart';
import 'forms.dart';
import 'models.dart';

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
      await observer.jumpTo(index: state.messages.length - 1);
    }
    if (!mounted || !scroll.hasClients) return;
    scroll.jumpTo(scroll.position.maxScrollExtent);
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
            title: Text(discText(a, 'name')),
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
          confirm: method == 'stop'
              ? context.l10n.discStop
              : context.l10n.discSave,
          tone: ConfirmTone.warning,
        )) {
      return;
    }
    try {
      await widget.api.call(method, params);
      await state.poll();
    } catch (e) {
      if (mounted) setState(() => error = e);
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
    );
    await state.poll();
  }

  Future<void> details() async {
    await showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      builder: (sheet) => DraggableScrollableSheet(
        expand: false,
        initialChildSize: 0.85,
        builder: (_, controller) => AnimatedBuilder(
          animation: state,
          builder: (_, _) {
            final l = sheet.l10n;
            final disc = state.discussion;
            final collab = state.collab;
            final host = disc['mirror'] != true;
            return ListView(
              controller: controller,
              padding: const EdgeInsets.all(16),
              children: [
                Text(l.discDetails, style: const TextStyle(fontSize: Fs.title)),
                if (discText(disc, 'description').isNotEmpty)
                  Text(discText(disc, 'description')),
                if (host) ...[
                  ListTile(
                    title: Text(l.discRename),
                    onTap: () async {
                      await discussionForm(
                        context,
                        widget.api,
                        l.discRename,
                        'update',
                        {'discussion': widget.room, ...disc},
                        {'title': l.discName, 'description': l.discDescription},
                      );
                      await state.poll();
                    },
                  ),
                  ListTile(
                    title: Text(
                      disc['status'] == 'archived'
                          ? l.discRestore
                          : l.discArchive,
                    ),
                    onTap: () => action('archive', {
                      'discussion': widget.room,
                      'archived': disc['status'] != 'archived',
                    }, title: l.discConfirmAction),
                  ),
                  SwitchListTile(
                    title: Text(l.discShare),
                    value: disc['shared'] == true,
                    onChanged: (v) => action('share', {
                      'discussion': widget.room,
                      'on': v,
                    }, title: v ? l.discShare : l.discUnshare),
                  ),
                  if (disc['shared'] == true)
                    ListTile(
                      title: Text(l.discInvite),
                      onTap: () async {
                        try {
                          final result = await widget.api.call('invite', {
                            'discussion': widget.room,
                          });
                          if (!mounted) return;
                          await showPosseDialog<void>(
                            context: context,
                            builder: (_) => AlertDialog(
                              title: Text(l.discInvite),
                              content: SelectableText(
                                discText(result, 'invite'),
                              ),
                              actions: [
                                TextButton(
                                  onPressed: () {
                                    Clipboard.setData(
                                      ClipboardData(
                                        text: discText(result, 'invite'),
                                      ),
                                    );
                                    Navigator.pop(context);
                                  },
                                  child: Text(l.discSave),
                                ),
                              ],
                            ),
                          );
                        } catch (e) {
                          if (mounted) setState(() => error = e);
                        }
                      },
                    ),
                ] else
                  ListTile(
                    title: Text(l.discLeave),
                    onTap: () => action('leave', {
                      'discussion': widget.room,
                    }, title: l.discLeave),
                  ),
                const Divider(),
                Text(l.discMembers, style: const TextStyle(fontSize: Fs.title)),
                if (writable)
                  ListTile(
                    leading: const Icon(Icons.person_add_rounded),
                    title: Text(l.discAddMember),
                    onTap: () => member(),
                  ),
                for (final m in state.members.where(
                  (m) => m['removedAt'] == null,
                )) ...[
                  ListTile(
                    title: Text(discText(m, 'name')),
                    subtitle: Text(
                      [
                        discText(m, 'agent'),
                        discText(m, 'role'),
                        discText(m, 'pausedText'),
                      ].where((s) => s.isNotEmpty).join(' · '),
                    ),
                    onTap: discMine(m, widget.self) && writable
                        ? () => member(m)
                        : null,
                  ),
                  if (discMine(m, widget.self))
                    Wrap(
                      spacing: 8,
                      children: [
                        if (writable) ...[
                          TextButton(
                            onPressed: () => action('member.restart', {
                              'member': m['id'],
                            }, title: l.discRestart),
                            child: Text(l.discRestart),
                          ),
                          if ([
                            'connection to the host was lost; confirm to continue',
                            'daemon restarted',
                          ].contains(m['paused']))
                            TextButton(
                              onPressed: () =>
                                  action('member.resume', {'member': m['id']}),
                              child: Text(l.discResume),
                            ),
                          TextButton(
                            onPressed: () => action('member.remove', {
                              'member': m['id'],
                            }, title: l.discRemove),
                            child: Text(l.discRemove),
                          ),
                        ],
                        if (discText(m, 'worker').isNotEmpty &&
                            ![
                              'gone',
                              'shell',
                              null,
                            ].contains(state.live[discText(m, 'worker')]))
                          TextButton(
                            onPressed: () => worker(m),
                            child: Text(l.discOpenWorker),
                          ),
                      ],
                    ),
                ],
                if (collab.isNotEmpty) ...[
                  const Divider(),
                  Text(
                    l.discPeople,
                    style: const TextStyle(fontSize: Fs.title),
                  ),
                  for (final p in discList(collab['people']))
                    ListTile(
                      title: Text(discText(p, 'name')),
                      subtitle: Text(
                        p['online'] == true
                            ? l.discOnline
                            : l.discPersonOffline,
                      ),
                      trailing:
                          host && p['host'] != true && p['state'] == 'active'
                          ? TextButton(
                              onPressed: () => action('person.remove', {
                                'discussion': widget.room,
                                'person': p['fp'],
                              }, title: l.discRemove),
                              child: Text(l.discRemove),
                            )
                          : null,
                    ),
                  for (final r in discList(
                    collab['requests'],
                  ).where((r) => r['state'] == 'pending'))
                    ListTile(
                      title: Text(discText(r, 'name')),
                      subtitle: Wrap(
                        spacing: 8,
                        children: [
                          TextButton(
                            onPressed: () => action('decide', {
                              'request': r['id'],
                              'approve': true,
                            }),
                            child: Text(l.discApprove),
                          ),
                          TextButton(
                            onPressed: () => action('decide', {
                              'request': r['id'],
                              'approve': false,
                            }),
                            child: Text(l.discReject),
                          ),
                        ],
                      ),
                    ),
                ],
                const Divider(),
                Text(l.discKey, style: const TextStyle(fontSize: Fs.title)),
                for (final m in state.keys)
                  ListTile(
                    title: Text(
                      discText(m, 'body'),
                      maxLines: 3,
                      overflow: TextOverflow.ellipsis,
                    ),
                    onTap: () {
                      Navigator.pop(sheet);
                      unawaited(locate(discText(m, 'id')));
                    },
                  ),
              ],
            );
          },
        ),
      ),
    );
    if (mounted) await state.poll();
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
                subtitle: Text(discText(m, 'authorName')),
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

  Widget message(DiscJson m) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final xs = state.executions
        .where((x) => x['message'] == m['id'] || x['id'] == m['execution'])
        .toList();
    final time = DateTime.tryParse(discText(m, 'createdAt'))?.toLocal();
    final canAnswer =
        m['kind'] == 'question' &&
        xs.any((x) => x['state'] == 'waiting' && discMine(x, widget.self)) &&
        !state.messages.any(
          (r) => r['kind'] == 'answer' && r['question'] == m['question'],
        );
    return Card(
      key: ValueKey(m['id']),
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    discText(m, 'authorName'),
                    style: TextStyle(color: c.muted, fontSize: Fs.small),
                  ),
                ),
                if (time != null)
                  Text(
                    monthDayTime(l, time, h24: use24Hour(context)),
                    style: TextStyle(color: c.muted, fontSize: Fs.small),
                  ),
                PopupMenuButton<String>(
                  onSelected: (v) {
                    if (v == 'quote') {
                      setState(() => quote = discText(m, 'id'));
                      scheduleSave();
                    } else {
                      unawaited(
                        action('key', {
                          'message': m['id'],
                          'on': m['keyAt'] == null,
                        }),
                      );
                    }
                  },
                  itemBuilder: (_) => [
                    if (writable)
                      PopupMenuItem(value: 'quote', child: Text(l.discQuote)),
                    if (writable)
                      PopupMenuItem(
                        value: 'key',
                        child: Text(
                          m['keyAt'] == null ? l.discMarkKey : l.discUnmarkKey,
                        ),
                      ),
                  ],
                ),
              ],
            ),
            if (discText(m, 'quote').isNotEmpty)
              TextButton(
                onPressed: () => locate(discText(m, 'quote')),
                child: Text(l.discQuote),
              ),
            if (discText(m, 'targetName').isNotEmpty)
              Text(
                '@${discText(m, 'targetName')}',
                style: TextStyle(color: c.accent),
              ),
            if (discText(m, 'mentionName').isNotEmpty)
              Text(
                '@${discText(m, 'mentionName')}',
                style: TextStyle(color: c.accent),
              ),
            if (m['keyAt'] != null)
              Text(
                l.discMarkKey,
                style: TextStyle(color: c.honey, fontSize: Fs.small),
              ),
            SelectionArea(
              child: MarkdownView(
                discText(m, 'body'),
                style: const TextStyle(fontSize: Fs.body, height: Lh.read),
              ),
            ),
            if (m['hasReport'] == true)
              TextButton(onPressed: () => report(m), child: Text(l.discReport)),
            for (final a in discList(m['attachments']))
              TextButton.icon(
                onPressed: () => download(m, a),
                icon: const Icon(Icons.attach_file_rounded, size: Ic.medium),
                label: Text(discText(a, 'name')),
              ),
            if (canAnswer && writable)
              FilledButton(
                onPressed: () => answer(m),
                child: Text(l.discAnswer),
              ),
            for (final x in xs) ...[
              const SizedBox(height: 8),
              Text(
                status(discText(x, 'state')),
                style: TextStyle(color: c.muted, fontSize: Fs.small),
              ),
              if (discText(x, 'reason').isNotEmpty)
                Text(
                  discText(x, 'reasonText'),
                  style: TextStyle(color: c.muted, fontSize: Fs.small),
                ),
              if (discMine(x, widget.self) && writable)
                Wrap(
                  spacing: 8,
                  children: [
                    if ([
                      'queued',
                      'held',
                      'blocked',
                      'claimed',
                    ].contains(x['state']))
                      TextButton(
                        onPressed: () =>
                            action('cancel', {'execution': x['id']}),
                        child: Text(l.discCancel),
                      ),
                    if (['starting', 'running', 'waiting'].contains(x['state']))
                      TextButton(
                        onPressed: () => action('stop', {
                          'execution': x['id'],
                        }, title: l.discStop),
                        child: Text(l.discStop),
                      ),
                    if ([
                      'failed',
                      'interrupted',
                      'stopped',
                      'cancelled',
                    ].contains(x['state']))
                      TextButton(
                        onPressed: () =>
                            action('retry', {'execution': x['id']}),
                        child: Text(l.discRetry),
                      ),
                    if (x['state'] == 'claimed')
                      TextButton(
                        onPressed: () => action(
                          'retry',
                          {'execution': x['id'], 'abandon': true},
                          title: l.discAbandon,
                          body: l.discAbandonWarning,
                        ),
                        child: Text(l.discAbandon),
                      ),
                    if (x['state'] == 'held') ...[
                      TextButton(
                        onPressed: () => action(
                          'confirm',
                          {'execution': x['id']},
                          title: l.discConfirm,
                          body: l.discConcurrentWarning,
                        ),
                        child: Text(l.discConfirm),
                      ),
                      TextButton(
                        onPressed: () => action('isolate', {
                          'execution': x['id'],
                        }, title: l.discIsolate),
                        child: Text(l.discIsolate),
                      ),
                    ],
                    if (x['terminal'] == true)
                      TextButton(
                        onPressed: () => worker(x),
                        child: Text(l.discOpenWorker),
                      ),
                  ],
                ),
            ],
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final locked = pending != null;
    final mine = state.members
        .where((m) => m['removedAt'] == null && discMine(m, widget.self))
        .toList();
    final people = discList(state.collab['people'])
        .where((p) => p['state'] == 'active')
        .toList();
    final shownError = error ?? state.error;
    final unavailable = state.discussion['status'] == 'archived'
        ? l.discReadOnly
        : state.collab['state'] == 'pending'
        ? l.discPending
        : ['removed', 'left', 'rejected'].contains(state.collab['state'])
        ? l.discRevoked
        : l.discOffline;
    return Scaffold(
      appBar: AppBar(
        title: Text(
          discText(state.discussion, 'title').isEmpty
              ? l.discTitle
              : discText(state.discussion, 'title'),
        ),
        actions: [
          IconButton(
            tooltip: l.discSearch,
            onPressed: search,
            icon: const Icon(Icons.search_rounded),
          ),
          IconButton(
            tooltip: l.discDetails,
            onPressed: details,
            icon: const Icon(Icons.info_outline_rounded),
          ),
        ],
      ),
      body: SafeArea(
        child: Column(
          children: [
            if (shownError != null)
              MaterialBanner(
                content: Text(friendlyError(shownError, l)),
                actions: [
                  TextButton(
                    onPressed: () {
                      setState(() => error = null);
                      state.dismissError();
                    },
                    child: Text(l.discClose),
                  ),
                ],
              ),
            if (!writable)
              Padding(
                padding: const EdgeInsets.all(8),
                child: Text(unavailable, style: TextStyle(color: c.muted)),
              ),
            if (state.loading) const LinearProgressIndicator(),
            if (state.more)
              TextButton(
                onPressed: state.loading ? null : earlier,
                child: Text(l.discEarlier),
              ),
            Expanded(
              child: state.messages.isEmpty
                  ? Center(child: Text(l.discNoMessages))
                  : ListViewObserver(
                      controller: observer,
                      child: ListView.builder(
                        controller: scroll,
                        itemCount: state.messages.length,
                        itemBuilder: (_, i) => message(state.messages[i]),
                      ),
                    ),
            ),
            if (state.moreAfter)
              Wrap(
                spacing: 8,
                children: [
                  TextButton(
                    onPressed: () => state.page(after: state.last),
                    child: Text(l.discLater),
                  ),
                  TextButton(onPressed: latest, child: Text(l.discLatest)),
                ],
              )
            else if (!atBottom && state.messages.isNotEmpty)
              TextButton(onPressed: bottom, child: Text(l.discLatest)),
            if (quote.isNotEmpty)
              ListTile(
                dense: true,
                title: Text(l.discQuote),
                onTap: () => locate(quote),
                trailing: locked
                    ? null
                    : IconButton(
                        onPressed: () {
                          setState(() => quote = '');
                          scheduleSave();
                        },
                        icon: const Icon(Icons.close_rounded),
                      ),
              ),
            if (attachments.isNotEmpty)
              SingleChildScrollView(
                scrollDirection: Axis.horizontal,
                child: Row(
                  children: [
                    for (final a in attachments)
                      Padding(
                        padding: const EdgeInsets.all(4),
                        child: InputChip(
                          label: Text(discText(a, 'name')),
                          onDeleted: locked ? null : () => removeAttachment(a),
                        ),
                      ),
                  ],
                ),
              ),
            if (uploading) LinearProgressIndicator(value: progress),
            if (locked)
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 16),
                child: Row(
                  children: [
                    Expanded(
                      child: Text(
                        l.discUnknown,
                        style: TextStyle(color: c.muted, fontSize: Fs.small),
                      ),
                    ),
                    TextButton(
                      onPressed: sending
                          ? null
                          : () async {
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
                            },
                      child: Text(l.discEditDraft),
                    ),
                  ],
                ),
              ),
            if (writable)
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 12),
                child: Row(
                  children: [
                    Expanded(
                      child: DropdownButton<String>(
                        value: mine.any((m) => m['id'] == target) ? target : '',
                        isExpanded: true,
                        items: [
                          DropdownMenuItem(
                            value: '',
                            child: Text(l.discNoTarget),
                          ),
                          for (final m in mine)
                            DropdownMenuItem(
                              value: discText(m, 'id'),
                              child: Text('@${discText(m, 'name')}'),
                            ),
                        ],
                        onChanged: locked
                            ? null
                            : (v) {
                                setState(() => target = v ?? '');
                                scheduleSave();
                              },
                      ),
                    ),
                    if (people.isNotEmpty)
                      PopupMenuButton<String>(
                        tooltip: l.discMention,
                        icon: Icon(
                          Icons.alternate_email_rounded,
                          color: mention.isEmpty ? c.muted : c.accent,
                        ),
                        enabled: !locked,
                        onSelected: (v) {
                          setState(() => mention = v);
                          scheduleSave();
                        },
                        itemBuilder: (_) => [
                          PopupMenuItem(value: '', child: Text(l.discCancel)),
                          for (final p in people)
                            PopupMenuItem(
                              value: discText(p, 'fp'),
                              child: Text(discText(p, 'name')),
                            ),
                        ],
                      ),
                  ],
                ),
              ),
            Padding(
              padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  IconButton(
                    tooltip: l.discAddFile,
                    onPressed: !writable || locked || uploading ? null : attach,
                    icon: const Icon(Icons.attach_file_rounded),
                  ),
                  IconButton(
                    tooltip: l.discAddPhoto,
                    onPressed: !writable || locked || uploading
                        ? null
                        : () => attach(photo: true),
                    icon: const Icon(Icons.photo_rounded),
                  ),
                  Expanded(
                    child: TextField(
                      controller: input,
                      enabled: writable && !locked,
                      minLines: 1,
                      maxLines: 5,
                      decoration: InputDecoration(hintText: l.discMessageHint),
                    ),
                  ),
                  IconButton(
                    tooltip: locked ? l.discRetry : l.discSend,
                    onPressed: !writable || sending || uploading ? null : send,
                    icon: Icon(
                      sending
                          ? Icons.hourglass_top_rounded
                          : locked
                          ? Icons.refresh_rounded
                          : Icons.send_rounded,
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
