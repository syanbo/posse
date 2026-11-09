part of 'room.dart';

/// 讨论详情：操作、成员、参与的人、关键消息，分组卡片，和设置页一个样子。
extension _DetailsViews on _DiscussionRoomState {
  String accessSummary(DiscJson m) {
    final l = context.l10n;
    final access = discText(m, 'access');
    if (access.isEmpty) return '';
    if (access == 'all') return l.discAccessEveryone;
    final names = [
      for (final fp in access.split(','))
        personName(fp).isEmpty ? fp : personName(fp),
    ];
    return l.discAccessPeople(names.join(', '));
  }

  Widget memberRow(DiscJson m, {VoidCallback? onTap}) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final mine = discMine(m, widget.self);
    final model = [
      discText(m, 'agent'),
      discText(m, 'model'),
    ].where((s) => s.isNotEmpty).join(' · ');
    final lines = [
      if (discText(m, 'role').isNotEmpty) discText(m, 'role'),
      model,
      if (!mine && personName(discText(m, 'owner')).isNotEmpty)
        l.discOnComputer(personName(discText(m, 'owner'))),
      if (mine && accessSummary(m).isNotEmpty) accessSummary(m),
    ];
    final paused = discText(m, 'pausedText');
    return RowTile(
      leading: AgentTile(agent: discText(m, 'agent'), size: Ic.large),
      title: Text(discText(m, 'name')),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(lines.join('\n'), maxLines: 4),
          if (paused.isNotEmpty)
            Text(paused, maxLines: 3, style: TextStyle(color: c.honey)),
        ],
      ),
      onTap: onTap,
    );
  }

  Future<void> details() async {
    await showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      backgroundColor: PosseColors.of(context).canvas,
      builder: (sheet) => DraggableScrollableSheet(
        expand: false,
        initialChildSize: 0.85,
        builder: (_, controller) => AnimatedBuilder(
          animation: state,
          builder: (_, _) {
            final l = sheet.l10n;
            final c = PosseColors.of(sheet);
            final disc = state.discussion;
            final collab = state.collab;
            final host = disc['mirror'] != true;
            final members = activeMembers;
            final people = discList(collab['people']);
            final requests = discList(collab['requests'])
                .where((r) => r['state'] == 'pending')
                .toList();
            return ListView(
              controller: controller,
              padding: const EdgeInsets.only(bottom: 24),
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(20, 0, 20, 16),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        discText(disc, 'title'),
                        style: TextStyle(
                          fontSize: Fs.title,
                          fontWeight: Fw.bold,
                          color: c.text,
                        ),
                      ),
                      if (discText(disc, 'description').isNotEmpty) ...[
                        const SizedBox(height: 4),
                        Text(
                          discText(disc, 'description'),
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ],
                    ],
                  ),
                ),
                Section(
                  title: l.discMembers,
                  trailing: writable
                      ? GestureDetector(
                          onTap: () => member(),
                          child: Icon(
                            Icons.add_rounded,
                            size: Ic.medium,
                            color: c.accent,
                          ),
                        )
                      : null,
                  inset: 16 + Ic.large + 12,
                  children: [
                    for (final m in members)
                      memberRow(
                        m,
                        onTap: discMine(m, widget.self)
                            ? () => memberActions(m)
                            : null,
                      ),
                    if (members.isEmpty && writable)
                      RowTile(
                        leading: Icon(
                          Icons.person_add_rounded,
                          color: c.accent,
                        ),
                        title: Text(l.discAddMember),
                        onTap: () => member(),
                      ),
                  ],
                ),
                if (people.isNotEmpty)
                  Section(
                    title: l.discPeople,
                    inset: 16 + Ic.large + 12,
                    children: [
                      for (final p in people)
                        RowTile(
                          leading: p['fp'] == selfFp
                              ? selfAvatar(Ic.large)
                              : personAvatar(p, discText(p, 'name'), Ic.large),
                          title: Text(
                            [
                              discText(p, 'name'),
                              if (p['fp'] == discText(collab, 'self'))
                                l.discYou,
                              if (p['host'] == true) l.discHost,
                            ].join(' · '),
                          ),
                          subtitle: Text(
                            p['online'] == true
                                ? l.discOnline
                                : l.discPersonOffline,
                            style: TextStyle(
                              color: p['online'] == true ? c.sage : c.muted,
                            ),
                          ),
                          // 点自己改名字和头像（旧电脑没有这个接口，不给点）
                          chevron: p['fp'] == selfFp && ownProfile.isNotEmpty,
                          onTap: p['fp'] == selfFp && ownProfile.isNotEmpty
                              ? editProfile
                              : null,
                          trailing:
                              host &&
                                  p['host'] != true &&
                                  p['fp'] != discText(collab, 'self') &&
                                  p['state'] == 'active'
                              ? TextButton(
                                  style: TextButton.styleFrom(
                                    backgroundColor: Colors.transparent,
                                    foregroundColor: c.danger,
                                  ),
                                  onPressed: () => action('person.remove', {
                                    'discussion': widget.room,
                                    'person': p['fp'],
                                  }, title: l.discRemove),
                                  child: Text(l.discRemove),
                                )
                              : null,
                        ),
                      for (final r in requests)
                        RowTile(
                          leading: initial(discText(r, 'name'), Ic.large),
                          title: Text(discText(r, 'name')),
                          chevron: false,
                          trailing: Row(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              TextButton(
                                onPressed: () => action('decide', {
                                  'request': r['id'],
                                  'approve': false,
                                }),
                                child: Text(l.discReject),
                              ),
                              FilledButton(
                                onPressed: () => action('decide', {
                                  'request': r['id'],
                                  'approve': true,
                                }),
                                child: Text(l.discApprove),
                              ),
                            ],
                          ),
                        ),
                    ],
                  ),
                if (state.keys.isNotEmpty)
                  Section(
                    title: l.discKey,
                    children: [
                      for (final m in state.keys)
                        RowTile(
                          leading: Icon(
                            Icons.star_rounded,
                            size: Ic.medium,
                            color: c.honey,
                          ),
                          title: Text(discAuthorName(m, state.collab)),
                          subtitle: Text(discText(m, 'body')),
                          onTap: () {
                            Navigator.pop(sheet);
                            unawaited(locate(discText(m, 'id')));
                          },
                        ),
                    ],
                  ),
                Section(
                  children: [
                    if (host) ...[
                      RowTile(
                        title: Text(l.discRename),
                        onTap: () async {
                          await discussionForm(
                            context,
                            widget.api,
                            l.discRename,
                            'update',
                            {'discussion': widget.room, ...disc},
                            {
                              'title': l.discName,
                              'description': l.discDescription,
                            },
                          );
                          await state.poll();
                        },
                      ),
                      RowTile(
                        title: Text(l.discShare),
                        chevron: false,
                        trailing: PosseSwitch(
                          value: disc['shared'] == true,
                          onChanged: (v) => action('share', {
                            'discussion': widget.room,
                            'on': v,
                          }, title: v ? l.discShare : l.discUnshare),
                        ),
                      ),
                      if (disc['shared'] == true)
                        RowTile(title: Text(l.discInvite), onTap: invite),
                      RowTile(
                        title: Text(
                          disc['status'] == 'archived'
                              ? l.discRestore
                              : l.discArchive,
                        ),
                        chevron: false,
                        onTap: () => action(
                          'archive',
                          {
                            'discussion': widget.room,
                            'archived': disc['status'] != 'archived',
                          },
                          title: disc['status'] == 'archived'
                              ? l.discRestore
                              : l.discArchive,
                        ),
                      ),
                    ] else
                      RowTile(
                        title: Text(
                          l.discLeave,
                          style: TextStyle(color: c.danger),
                        ),
                        chevron: false,
                        onTap: () => action('leave', {
                          'discussion': widget.room,
                        }, title: l.discLeave),
                      ),
                  ],
                ),
              ],
            );
          },
        ),
      ),
    );
    if (mounted) await state.poll();
  }

  Future<void> invite() async {
    final l = context.l10n;
    try {
      final result = await widget.api.call('invite', {
        'discussion': widget.room,
      });
      if (!mounted) return;
      final code = discText(result, 'invite');
      await showPosseDialog<void>(
        context: context,
        builder: (dialog) => AlertDialog(
          title: Text(l.discInvite),
          content: SelectableText(code),
          actions: [
            TextButton(
              onPressed: () {
                Clipboard.setData(ClipboardData(text: code));
                Navigator.pop(dialog);
              },
              child: Text(l.discCopy),
            ),
          ],
        ),
      );
    } catch (e) {
      if (mounted) update(() => error = e);
    }
  }

  /// 点自己的 Agent：编辑、打开终端、重启、继续、移除。
  Future<void> memberActions(DiscJson m) async {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final live =
        discText(m, 'worker').isNotEmpty &&
        !['gone', 'shell', null].contains(state.live[discText(m, 'worker')]);
    final resumable = [
      'connection to the host was lost; confirm to continue',
      'daemon restarted',
    ].contains(m['paused']);
    final picked = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (sheet) => SafeArea(
        child: Section(
          title: discText(m, 'name'),
          children: [
            if (writable)
              RowTile(
                title: Text(l.discEdit),
                onTap: () => Navigator.pop(sheet, 'edit'),
              ),
            if (live)
              RowTile(
                title: Text(l.discOpenWorker),
                onTap: () => Navigator.pop(sheet, 'open'),
              ),
            if (writable && resumable)
              RowTile(
                title: Text(l.discResume),
                chevron: false,
                onTap: () => Navigator.pop(sheet, 'resume'),
              ),
            if (writable)
              RowTile(
                title: Text(l.discRestart),
                chevron: false,
                onTap: () => Navigator.pop(sheet, 'restart'),
              ),
            if (writable)
              RowTile(
                title: Text(l.discRemove, style: TextStyle(color: c.danger)),
                chevron: false,
                onTap: () => Navigator.pop(sheet, 'remove'),
              ),
          ],
        ),
      ),
    );
    if (!mounted) return;
    switch (picked) {
      case 'edit':
        await member(m);
      case 'open':
        worker(m);
      case 'resume':
        await action('member.resume', {'member': m['id']});
      case 'restart':
        await action('member.restart', {
          'member': m['id'],
        }, title: l.discRestart);
      case 'remove':
        await action('member.remove', {'member': m['id']}, title: l.discRemove);
    }
  }
}
