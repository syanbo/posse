part of 'room.dart';

/// 讨论详情：推一整页，分组卡片，和设置页一个样子。点自己的 Agent 直接进它的编辑页。
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
    // 第二行是怎么跑的：模型、思考深度，别人的写在谁的电脑上，自己的写还有谁能 @
    final meta = [
      [
        discText(m, 'model'),
        discText(m, 'effort'),
      ].where((s) => s.isNotEmpty).join(' '),
      if (!mine && personName(discText(m, 'owner')).isNotEmpty)
        l.discOnComputer(personName(discText(m, 'owner'))),
      if (mine) accessSummary(m),
    ].where((s) => s.isNotEmpty).join(' · ');
    final paused = discText(m, 'pausedText');
    return RowTile(
      leading: AgentTile(agent: discText(m, 'agent'), size: Ic.large),
      title: Text(discText(m, 'name')),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (discText(m, 'role').isNotEmpty)
            Text(discText(m, 'role'), maxLines: 2),
          if (meta.isNotEmpty)
            Text(meta, maxLines: 1, style: TextStyle(color: c.faint)),
          if (paused.isNotEmpty)
            Text(paused, maxLines: 3, style: TextStyle(color: c.honey)),
        ],
      ),
      onTap: onTap,
    );
  }

  Future<void> details() async {
    await Navigator.push<void>(
      context,
      MaterialPageRoute(
        builder: (sheet) => AnimatedBuilder(
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
            final editable = host && writable;
            return Scaffold(
              appBar: AppBar(title: Text(l.discDetails)),
              body: SideSafe(
                child: ListView(
                  padding: const EdgeInsets.only(top: 8, bottom: 24),
                  children: [
                    // 标题和说明：房主点它改
                    Section(
                      children: [
                        InkWell(
                          onTap: editable ? () => rename(disc) : null,
                          highlightColor: c.wash,
                          child: Padding(
                            padding: const EdgeInsets.fromLTRB(16, 14, 10, 14),
                            child: Row(
                              children: [
                                Expanded(
                                  child: Column(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.start,
                                    children: [
                                      Text(
                                        discText(disc, 'title'),
                                        style: TextStyle(
                                          fontSize: Fs.title,
                                          fontWeight: Fw.bold,
                                          color: c.text,
                                        ),
                                      ),
                                      if (discText(
                                        disc,
                                        'description',
                                      ).isNotEmpty) ...[
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
                                if (editable)
                                  Icon(
                                    Icons.chevron_right_rounded,
                                    size: Ic.medium,
                                    color: c.faint,
                                  ),
                              ],
                            ),
                          ),
                        ),
                      ],
                    ),
                    if (discText(disc, 'summary').isNotEmpty)
                      Section(
                        title: l.discSummary,
                        children: [
                          Padding(
                            padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
                            child: MarkdownView(
                              discText(disc, 'summary'),
                              style: TextStyle(
                                fontSize: Fs.small,
                                height: Lh.body,
                                color: c.text,
                              ),
                            ),
                          ),
                        ],
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
                          memberRow(m, onTap: memberTap(m)),
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
                                  : personAvatar(
                                      p,
                                      discText(p, 'name'),
                                      Ic.large,
                                    ),
                              title: Text(
                                [
                                  discText(p, 'name'),
                                  if (p['fp'] == discText(collab, 'self'))
                                    l.discYou,
                                  if (p['host'] == true) l.discHost,
                                  if (p['role'] == 'guest') l.discGuest,
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
                              chevron:
                                  p['fp'] == selfFp && ownProfile.isNotEmpty,
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
                              title: Text(
                                [
                                  discText(r, 'name'),
                                  if (r['role'] == 'guest') l.discGuest,
                                ].join(' · '),
                              ),
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
                        if (writable && members.any(canUse))
                          RowTile(
                            title: Text(l.discSummarize),
                            chevron: false,
                            onTap: () => summarize(sheet),
                          ),
                        if (editable && discText(disc, 'summary').isNotEmpty)
                          RowTile(
                            title: Text(l.discSummaryClear),
                            chevron: false,
                            onTap: () => action('summary.clear', {
                              'discussion': widget.room,
                            }, title: l.discSummaryClear),
                          ),
                        if (host) ...[
                          RowTile(
                            title: Text(l.discShare),
                            chevron: false,
                            trailing: PosseSwitch(
                              value: disc['shared'] == true,
                              onChanged: (v) => action(
                                'share',
                                {'discussion': widget.room, 'on': v},
                                title: v ? l.discShare : l.discUnshare,
                                // 确认按钮是等宽的两个之一，长的「停止局域网分享」放不下
                                confirm: v
                                    ? l.discShareConfirm
                                    : l.discUnshareConfirm,
                              ),
                            ),
                          ),
                          if (disc['shared'] == true) ...[
                            RowTile(title: Text(l.discInvite), onTap: invite),
                            RowTile(
                              title: Text(l.discInviteGuest),
                              onTap: () => invite(guest: true),
                            ),
                          ],
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
                ),
              ),
            );
          },
        ),
      ),
    );
    if (mounted) await state.poll();
  }

  /// 摘要由成员写：挑一个能用的 Agent（只有一个就直接用它），给它发一条请求，它存好摘要再回复。
  Future<void> summarize(BuildContext sheet) async {
    final l = context.l10n;
    final usable = activeMembers.where(canUse).toList();
    var target = usable.length == 1 ? discText(usable.first, 'id') : null;
    target ??= await showModalBottomSheet<String>(
      context: sheet,
      showDragHandle: true,
      builder: (picker) => SafeArea(
        child: Section(
          children: [
            for (final m in usable)
              RowTile(
                leading: AgentTile(agent: discText(m, 'agent'), size: Ic.large),
                title: Text(discText(m, 'name')),
                chevron: false,
                onTap: () => Navigator.pop(picker, discText(m, 'id')),
              ),
          ],
        ),
      ),
    );
    if (target == null || !mounted) return;
    await action('post', {
      'discussion': widget.room,
      'target': target,
      'body': l.discSummarizeRequest,
      'clientId': discRequestId(),
    });
    if (sheet.mounted) Navigator.pop(sheet);
  }

  Future<void> invite({bool guest = false}) async {
    final l = context.l10n;
    try {
      final result = await widget.api.call('invite', {
        'discussion': widget.room,
        'guest': guest,
      });
      if (!mounted) return;
      final code = discText(result, 'invite');
      await showPosseDialog<void>(
        context: context,
        builder: (dialog) => AlertDialog(
          title: Text(guest ? l.discInviteGuest : l.discInvite),
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

  /// 点成员：自己的进编辑页（打开终端、重启、移除也在那里）；不能改时（归档、断线）有终端就直接打开。
  VoidCallback? memberTap(DiscJson m) {
    if (!discMine(m, widget.self)) return null;
    if (writable) return () => member(m);
    final live =
        discText(m, 'worker').isNotEmpty &&
        !['gone', 'shell', null].contains(state.live[discText(m, 'worker')]);
    return live ? () => worker(m) : null;
  }

  Future<void> rename(DiscJson disc) async {
    final l = context.l10n;
    await discussionForm(
      context,
      widget.api,
      l.discRename,
      'update',
      {'discussion': widget.room, ...disc},
      {'title': l.discName, 'description': l.discDescription},
    );
    await state.poll();
  }
}
