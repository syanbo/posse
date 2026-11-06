import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
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

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
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
      body: SafeArea(
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.all(12),
              child: SegmentedButton<bool>(
                segments: [
                  ButtonSegment(value: false, label: Text(l.discActive)),
                  ButtonSegment(value: true, label: Text(l.discArchived)),
                ],
                selected: {archived},
                onSelectionChanged: (v) => setState(() => archived = v.first),
              ),
            ),
            if (error != null)
              MaterialBanner(
                content: Text(friendlyError(error!, l)),
                actions: [
                  TextButton(onPressed: load, child: Text(l.discRetry)),
                  TextButton(
                    onPressed: () => setState(() => error = null),
                    child: Text(l.discClose),
                  ),
                ],
              ),
            Expanded(
              child: !loaded
                  ? const Center(child: CircularProgressIndicator())
                  : rows.isEmpty
                  ? Center(child: Text(l.discEmpty))
                  : RefreshIndicator(
                      onRefresh: () {
                        seq = 0;
                        return load();
                      },
                      child: ListView.builder(
                        itemCount: rows.length,
                        itemBuilder: (context, index) {
                          final row = rows[index];
                          final last = discMap(row['last']);
                          final unread =
                              discInt(row, 'msgSeq') >
                              (reads[discText(row, 'key')] ?? 0);
                          return ListTile(
                            onTap: () => open(row),
                            leading: Icon(
                              Icons.forum_rounded,
                              color: discInt(row, 'questions') > 0
                                  ? c.honey
                                  : c.accent,
                            ),
                            title: Text(discText(row, 'title')),
                            subtitle: Text(
                              discInt(row, 'questions') > 0
                                  ? l.discWaiting
                                  : discText(last, 'body'),
                              maxLines: 2,
                              overflow: TextOverflow.ellipsis,
                            ),
                            trailing: unread
                                ? Text(
                                    l.discUnread,
                                    style: TextStyle(
                                      color: c.accent,
                                      fontSize: Fs.small,
                                    ),
                                  )
                                : null,
                          );
                        },
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}
