import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'api.dart';
import 'forms.dart';
import 'models.dart';
import 'room.dart';

class DiscussionsScreen extends StatefulWidget {
  const DiscussionsScreen({super.key, required this.model});
  final PosseAppModel model;
  @override
  State<DiscussionsScreen> createState() => _DiscussionsScreenState();
}

class _DiscussionsScreenState extends State<DiscussionsScreen>
    with WidgetsBindingObserver {
  late final api = DiscussionApi(widget.model);
  Timer? timer;
  List<DiscJson> items = [];
  final reads = <String, int>{};
  String self = '';
  int seq = 0;
  bool busy = false, loaded = false, archived = false, foreground = true;
  Object? error;
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    unawaited(load());
    timer = Timer.periodic(const Duration(seconds: 2), (_) {
      if (foreground && ModalRoute.of(context)?.isCurrent == true) {
        unawaited(load());
      }
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    foreground = state == AppLifecycleState.resumed;
    if (foreground) unawaited(load());
  }

  @override
  void dispose() {
    timer?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  Future<void> load() async {
    if (busy) return;
    if (!api.connected && loaded) {
      if (mounted) setState(() {});
      return;
    }
    busy = true;
    try {
      final result = await api.call('list', {'since': seq});
      final next = result.containsKey('items')
          ? discList(result['items'])
          : items;
      final readNext = <String, int>{};
      for (final item in next) {
        final key = discText(item, 'key');
        final saved = await DiscussionStorage(api.computer ?? '', key).read();
        readNext[key] = discInt(saved, 'read');
      }
      if (!mounted) return;
      setState(() {
        items = next;
        seq = discInt(result, 'seq');
        self = discText(result, 'self');
        reads.addAll(readNext);
        loaded = true;
      });
    } catch (e) {
      if (mounted) {
        setState(() {
          error = e;
          loaded = true;
        });
      }
    } finally {
      busy = false;
    }
  }

  Future<void> create() async {
    final l = context.l10n;
    final result = await discussionForm(context, api, l.discNew, 'create', {}, {
      'title': l.discName,
      'description': l.discDescription,
    }, workspace: true);
    if (!mounted || result == null) return;
    seq = 0;
    await load();
    if (!mounted) return;
    await open(result);
  }

  Future<void> join() async {
    final l = context.l10n;
    if (await discussionForm(context, api, l.discJoin, 'join', {}, {
          'invite': l.discInviteCode,
        }) !=
        null) {
      seq = 0;
      await load();
    }
  }

  Future<void> open(DiscJson item) async {
    await Navigator.push<void>(
      context,
      MaterialPageRoute(
        builder: (_) =>
            DiscussionRoom(api: api, room: discText(item, 'key'), self: self),
      ),
    );
    await load();
  }

  /// 列表里的时间：今天写钟点，更早写日期。
  String stamp(DiscJson last) {
    final time = DateTime.tryParse(discText(last, 'createdAt'))?.toLocal();
    if (time == null) return '';
    final now = DateTime.now();
    final l = context.l10n;
    final h24 = use24Hour(context);
    return time.year == now.year &&
            time.month == now.month &&
            time.day == now.day
        ? clockTime(l, time, h24: h24)
        : monthDayTime(l, time, h24: h24);
  }

  Widget row(DiscJson row) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final last = discMap(row['last']);
    final read = reads[discText(row, 'key')] ?? 0;
    final unread = discInt(row, 'msgSeq') > read;
    final mentioned = discInt(row, 'mentionSeq') > read;
    final questions = discInt(row, 'questions') > 0;
    final author = discText(last, 'authorName');
    final body = discText(last, 'body').replaceAll('\n', ' ');
    return InkWell(
      onTap: () => open(row),
      highlightColor: c.wash,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 14, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    discText(row, 'title'),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: Fs.body,
                      fontWeight: unread ? Fw.bold : Fw.medium,
                      color: c.text,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  stamp(last),
                  style: TextStyle(fontSize: Fs.small, color: c.faint),
                ),
              ],
            ),
            const SizedBox(height: 4),
            Row(
              children: [
                Expanded(
                  child: Text(
                    questions
                        ? l.discWaiting
                        : author.isEmpty
                        ? body
                        : '$author: $body',
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: Fs.small,
                      height: Lh.body,
                      color: questions ? c.honey : c.muted,
                    ),
                  ),
                ),
                if (mentioned)
                  Padding(
                    padding: const EdgeInsets.only(left: 8),
                    child: Container(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 6,
                        vertical: 2,
                      ),
                      decoration: BoxDecoration(
                        color: c.soft(c.accent),
                        borderRadius: BorderRadius.circular(Rd.small),
                      ),
                      child: Text(
                        l.discAtYou,
                        style: TextStyle(fontSize: Fs.small, color: c.accent),
                      ),
                    ),
                  )
                else if (unread)
                  Padding(
                    padding: const EdgeInsets.only(left: 8),
                    child: Container(
                      width: 8,
                      height: 8,
                      decoration: BoxDecoration(
                        color: c.accent,
                        shape: BoxShape.circle,
                      ),
                    ),
                  ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final rows = items
        .where((x) => (x['status'] == 'archived') == archived)
        .toList();
    return Scaffold(
      appBar: AppBar(
        title: Text(l.discTitle),
        actions: [
          IconButton(
            tooltip: l.discJoin,
            onPressed: join,
            icon: const Icon(Icons.group_add_rounded),
          ),
          IconButton(
            tooltip: l.discNew,
            onPressed: create,
            icon: const Icon(Icons.add_rounded),
          ),
        ],
      ),
      body: SideSafe(
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 12),
              child: Segmented(
                labels: [l.discActive, l.discArchived],
                selected: archived ? 1 : 0,
                onChanged: (i) => setState(() => archived = i == 1),
              ),
            ),
            if (error != null)
              NoticeBar(
                friendlyError(error!, l),
                onClose: () => setState(() => error = null),
              ),
            Expanded(
              child: !loaded
                  ? const Center(child: HexSpinner(size: Ic.hero))
                  : RefreshIndicator(
                      onRefresh: () {
                        seq = 0;
                        return load();
                      },
                      child: rows.isEmpty
                          ? ListView(
                              children: [
                                EmptyState(
                                  icon: Icons.forum_rounded,
                                  text: l.discEmpty,
                                ),
                              ],
                            )
                          : CustomScrollView(
                              slivers: [
                                SliverSection(
                                  itemCount: rows.length,
                                  itemBuilder: (_, i) => row(rows[i]),
                                ),
                              ],
                            ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}
