part of 'room.dart';

/// 消息行和它下面的执行状态。和桌面一致：自己的话靠右、不铺底；Agent 和别人的话靠左，
/// 名字在上；已经结束的执行收成一行小字。
extension _MessageViews on _DiscussionRoomState {
  /// 自己发的话（发言、回答）：作者是本机的人。别人发的带着他的指纹。
  bool own(DiscJson m) {
    final author = discText(m, 'authorId');
    final other =
        discText(m, 'authorName').isNotEmpty &&
        author.isNotEmpty &&
        (state.collab.isEmpty || author != discText(state.collab, 'self'));
    return !other && (m['kind'] == 'user' || m['kind'] == 'answer');
  }

  String stamp(DiscJson m) {
    final time = DateTime.tryParse(discText(m, 'createdAt'))?.toLocal();
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

  Widget message(DiscJson m) {
    final c = PosseColors.of(context);
    final mine = own(m);
    // 一轮执行只在最后一条提到它的消息下面显示一次（请求、提问、回复都带着它）
    final at = state.messages.indexOf(m);
    final xs = state.executions
        .where((x) => x['message'] == m['id'] || x['id'] == m['execution'])
        .where(
          (x) => !state.messages
              .skip(at + 1)
              .any((later) => later['execution'] == x['id']),
        )
        .toList();
    final canAnswer =
        writable &&
        m['kind'] == 'question' &&
        answering != discText(m, 'question') &&
        pending == null &&
        xs.any((x) => x['state'] == 'waiting' && discMine(x, widget.self)) &&
        !state.messages.any(
          (r) => r['kind'] == 'answer' && r['question'] == m['question'],
        );
    final body = discText(m, 'body');
    final align = mine ? CrossAxisAlignment.end : CrossAxisAlignment.start;
    final tags = [
      if (discText(m, 'targetName').isNotEmpty) discText(m, 'targetName'),
      if (discText(m, 'mentionName').isNotEmpty) discText(m, 'mentionName'),
    ];
    final header = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (!mine) ...[
          avatar(m),
          const SizedBox(width: 8),
          Flexible(
            child: Text(
              discAuthorName(m, state.collab),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: c.text,
              ),
            ),
          ),
          const SizedBox(width: 8),
        ],
        if (m['keyAt'] != null) ...[
          Icon(Icons.star_rounded, size: Ic.inline, color: c.honey),
          const SizedBox(width: 4),
        ],
        Text(
          stamp(m),
          style: TextStyle(fontSize: Fs.small, color: c.faint),
        ),
        if (mine) ...[const SizedBox(width: 8), selfAvatar(_avatar)],
      ],
    );
    // 正文缩进到名字下面；自己的话靠右，右边让出头像的宽度
    EdgeInsets indent(double top) => EdgeInsets.only(
      left: mine ? 0 : _bodyIndent,
      right: mine ? _bodyIndent : 0,
      top: top,
    );
    return GestureDetector(
      key: ValueKey(m['id']),
      behavior: HitTestBehavior.opaque,
      onLongPress: () => messageActions(m),
      child: Padding(
        padding: EdgeInsets.fromLTRB(mine ? _ownIndent : 16, 10, 16, 10),
        child: Column(
          crossAxisAlignment: align,
          children: [
            header,
            if (discText(m, 'quote').isNotEmpty)
              Padding(padding: indent(6), child: quoted(discText(m, 'quote'))),
            if (tags.isNotEmpty)
              Padding(
                padding: indent(6),
                child: Wrap(
                  spacing: 6,
                  runSpacing: 4,
                  children: [for (final t in tags) pill('@$t', c.accent)],
                ),
              ),
            if (body.trim().isNotEmpty)
              Padding(
                padding: indent(4),
                // 不包 SelectionArea：长按整条消息出引用、标为关键、复制，选字会抢走长按
                child: MarkdownView(
                  body,
                  style: TextStyle(
                    fontSize: Fs.body,
                    height: Lh.read,
                    color: c.text,
                  ),
                ),
              ),
            if (discList(m['attachments']).isNotEmpty || m['hasReport'] == true)
              Padding(
                padding: indent(8),
                child: Wrap(
                  spacing: 8,
                  runSpacing: 8,
                  alignment: mine ? WrapAlignment.end : WrapAlignment.start,
                  children: [
                    for (final a in discList(m['attachments']))
                      attachment(m, a),
                    if (m['hasReport'] == true)
                      fileChip(
                        Icons.description_rounded,
                        context.l10n.discReport,
                        '',
                        () => report(m),
                      ),
                  ],
                ),
              ),
            if (canAnswer)
              Padding(
                padding: indent(8),
                child: FilledButton(
                  style: FilledButton.styleFrom(
                    minimumSize: const Size(0, 34),
                    padding: const EdgeInsets.symmetric(horizontal: 16),
                    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                  ),
                  onPressed: () => answer(m),
                  child: Text(context.l10n.discAnswer),
                ),
              ),
            for (final x in xs)
              Padding(padding: indent(6), child: execution(x, mine)),
          ],
        ),
      ),
    );
  }

  /// 作者的小头像：Agent 用它的色块，人用他的头像，没有头像画名字的第一个字。
  Widget avatar(DiscJson m) {
    if (discAgentAuthor(m)) {
      return AgentTile(agent: discText(m, 'authorAgent'), size: _avatar);
    }
    final fp = discText(m, 'authorId');
    if (fp.isNotEmpty && fp == selfFp) return selfAvatar(_avatar);
    return personAvatar(
      discPerson(state.collab, fp) ?? {},
      discAuthorName(m, state.collab),
      _avatar,
    );
  }

  Widget personAvatar(DiscJson person, String name, double size) =>
      PersonAvatar(
        call: widget.api.call,
        sha: discText(person, 'avatar'),
        name: name,
        size: size,
      );

  /// 自己的头像：先用这台电脑现在的资料（房主那边可能还是旧的），旧电脑取不到时用讨论里记的。
  Widget selfAvatar(double size) {
    final p = ownProfile.isNotEmpty
        ? ownProfile
        : discPerson(state.collab, selfFp) ?? {};
    return personAvatar(p, discText(p, 'name'), size);
  }

  Widget initial(String name, double size) =>
      DiscInitial(name: name, size: size);

  Widget pill(String text, Color color) {
    final c = PosseColors.of(context);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      decoration: BoxDecoration(
        color: c.soft(color),
        borderRadius: BorderRadius.circular(Rd.small),
      ),
      child: Text(
        text,
        style: TextStyle(fontSize: Fs.small, color: color),
      ),
    );
  }

  /// 引用的那条：作者和开头一行，点一下跳过去。
  Widget quoted(String id) {
    final c = PosseColors.of(context);
    final q = state.messages.where((m) => m['id'] == id).firstOrNull;
    final text = q == null
        ? context.l10n.discQuote
        : '${discAuthorName(q, state.collab)}  ${plainSnippet(discText(q, 'body'))}';
    return GestureDetector(
      onTap: () => locate(id),
      child: Container(
        constraints: const BoxConstraints(maxWidth: 280),
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
        decoration: BoxDecoration(
          color: c.fill,
          borderRadius: BorderRadius.circular(Rd.button),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.format_quote_rounded, size: Ic.inline, color: c.faint),
            const SizedBox(width: 4),
            Flexible(
              child: Text(
                text,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: Fs.small, color: c.muted),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget attachment(DiscJson m, DiscJson a) {
    final image = discText(a, 'type').startsWith('image/');
    if (image) {
      return DiscThumbView(
        key: ValueKey('${m['id']}/${a['id']}'),
        thumbs: thumbs,
        room: widget.room,
        message: discText(m, 'id'),
        attachment: a,
        onTap: () => download(m, a),
        fallback: fileChip(
          Icons.image_rounded,
          discText(a, 'name'),
          formatBytes(discInt(a, 'size')),
          () => download(m, a),
        ),
      );
    }
    return fileChip(
      Icons.insert_drive_file_rounded,
      discText(a, 'name'),
      formatBytes(discInt(a, 'size')),
      () => download(m, a),
    );
  }

  Widget fileChip(IconData icon, String name, String size, VoidCallback onTap) {
    final c = PosseColors.of(context);
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: Ic.small, color: c.muted),
              const SizedBox(width: 6),
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 180),
                child: Text(
                  name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(fontSize: Fs.small, color: c.text),
                ),
              ),
              if (size.isNotEmpty) ...[
                const SizedBox(width: 6),
                Text(
                  size,
                  style: TextStyle(fontSize: Fs.small, color: c.faint),
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  /// 一轮执行：状态图形、谁、状态，原因跟在后面；自己的执行能做的操作是一排文字按钮。
  Widget execution(DiscJson x, bool mine) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final st = discText(x, 'state');
    final member = state.members
        .where((m) => m['id'] == x['member'])
        .firstOrNull;
    final name = member == null ? '' : discText(member, 'name');
    final (Widget glyph, Color color) = switch (st) {
      'starting' ||
      'running' => (HexSpinner(size: Ic.glyph, color: c.accent), c.muted),
      'waiting' || 'held' || 'blocked' => (
        Icon(Icons.pending_rounded, size: Ic.inline, color: c.honey),
        c.honey,
      ),
      'done' => (
        Icon(Icons.check_circle_rounded, size: Ic.inline, color: c.sage),
        c.muted,
      ),
      'failed' || 'interrupted' || 'revoked' => (
        Icon(Icons.error_rounded, size: Ic.inline, color: c.danger),
        c.danger,
      ),
      _ => (
        Icon(Icons.schedule_rounded, size: Ic.inline, color: c.faint),
        c.muted,
      ),
    };
    final reason = discText(x, 'reasonText');
    final line = [
      if (name.isNotEmpty) name,
      status(st),
      if (reason.isNotEmpty) reason,
    ].join(' · ');
    final actions = <(String, VoidCallback)>[];
    if (discMine(x, widget.self) && writable) {
      if (['queued', 'held', 'blocked', 'claimed'].contains(st)) {
        actions.add((
          l.discCancel,
          () => action('cancel', {'execution': x['id']}),
        ));
      }
      if (['starting', 'running', 'waiting'].contains(st)) {
        actions.add((
          l.discStop,
          () => action('stop', {'execution': x['id']}, title: l.discStop),
        ));
      }
      if (['failed', 'interrupted', 'stopped', 'cancelled'].contains(st)) {
        actions.add((
          l.discRetry,
          () => action('retry', {'execution': x['id']}),
        ));
      }
      if (st == 'claimed') {
        actions.add((
          l.discAbandon,
          () => action(
            'retry',
            {'execution': x['id'], 'abandon': true},
            title: l.discAbandon,
            body: l.discAbandonWarning,
          ),
        ));
      }
      if (st == 'held') {
        actions.add((
          l.discConfirm,
          () => action(
            'confirm',
            {'execution': x['id']},
            title: l.discConfirm,
            body: l.discConcurrentWarning,
          ),
        ));
        actions.add((
          l.discIsolate,
          () => action('isolate', {'execution': x['id']}, title: l.discIsolate),
        ));
      }
      if (x['terminal'] == true) {
        actions.add((l.discOpenWorker, () => worker(x)));
      }
    }
    return Column(
      crossAxisAlignment: mine
          ? CrossAxisAlignment.end
          : CrossAxisAlignment.start,
      children: [
        Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox(
              width: Ic.inline,
              height: Ic.inline,
              child: Center(child: glyph),
            ),
            const SizedBox(width: 6),
            Flexible(
              child: Text(
                line,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: Fs.small, color: color),
              ),
            ),
          ],
        ),
        if (actions.isNotEmpty)
          Padding(
            padding: const EdgeInsets.only(left: Ic.inline + 6, top: 2),
            child: Wrap(
              spacing: 16,
              children: [
                for (final (label, onTap) in actions)
                  GestureDetector(
                    onTap: onTap,
                    child: Padding(
                      padding: const EdgeInsets.symmetric(vertical: 4),
                      child: Text(
                        label,
                        style: TextStyle(
                          fontSize: Fs.small,
                          fontWeight: Fw.medium,
                          color: c.accent,
                        ),
                      ),
                    ),
                  ),
              ],
            ),
          ),
      ],
    );
  }

  /// 长按消息：引用、标为关键、复制。
  Future<void> messageActions(DiscJson m) async {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final picked = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (sheet) => SafeArea(
        child: Section(
          children: [
            if (writable)
              RowTile(
                leading: Icon(Icons.format_quote_rounded, color: c.muted),
                title: Text(l.discQuote),
                chevron: false,
                onTap: () => Navigator.pop(sheet, 'quote'),
              ),
            if (writable)
              RowTile(
                leading: Icon(
                  m['keyAt'] == null
                      ? Icons.star_outline_rounded
                      : Icons.star_rounded,
                  color: c.honey,
                ),
                title: Text(
                  m['keyAt'] == null ? l.discMarkKey : l.discUnmarkKey,
                ),
                chevron: false,
                onTap: () => Navigator.pop(sheet, 'key'),
              ),
            RowTile(
              leading: Icon(Icons.copy_rounded, color: c.muted),
              title: Text(l.discCopy),
              chevron: false,
              onTap: () => Navigator.pop(sheet, 'copy'),
            ),
          ],
        ),
      ),
    );
    if (!mounted) return;
    switch (picked) {
      case 'quote':
        update(() {
          quote = discText(m, 'id');
          answering = '';
          answerTo = '';
        });
        focus.requestFocus();
        scheduleSave();
      case 'key':
        await action('key', {'message': m['id'], 'on': m['keyAt'] == null});
      case 'copy':
        await Clipboard.setData(ClipboardData(text: discText(m, 'body')));
    }
  }
}

/// 一行摘要：去掉 Markdown 的标记和换行。
String plainSnippet(String body) => body
    .replaceAll(RegExp(r'^\s*(#+|[-*+]|\d+\.)\s+', multiLine: true), '')
    .replaceAll(RegExp(r'[`*_>]'), '')
    .replaceAll(RegExp(r'\s+'), ' ')
    .trim();

/// 头像的边长；正文缩进到名字下面。
const double _avatar = 20;
const double _bodyIndent = _avatar + 8;

/// 消息里图片附件的缩略图：常驻进程缩好再给，按原图比例占位，读不到时换成文件条。
class DiscThumbView extends StatefulWidget {
  const DiscThumbView({
    super.key,
    required this.thumbs,
    required this.room,
    required this.message,
    required this.attachment,
    required this.onTap,
    required this.fallback,
  });
  final DiscThumbs thumbs;
  final String room, message;
  final DiscJson attachment;
  final VoidCallback onTap;
  final Widget fallback;
  @override
  State<DiscThumbView> createState() => _DiscThumbViewState();
}

class _DiscThumbViewState extends State<DiscThumbView> {
  late Future<DiscThumb?> thumb = widget.thumbs.get(
    widget.room,
    widget.message,
    discText(widget.attachment, 'id'),
  );

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return FutureBuilder<DiscThumb?>(
      future: thumb,
      builder: (context, snap) {
        final t = snap.data;
        if (snap.connectionState == ConnectionState.done && t == null) {
          return widget.fallback;
        }
        // 宽不超过 240、高不超过 280，按原图比例
        var w = 240.0, h = 160.0;
        if (t != null && t.width > 0 && t.height > 0) {
          final k = [240 / t.width, 280 / t.height, 1.0].reduce(min);
          w = t.width * k;
          h = t.height * k;
        }
        return GestureDetector(
          onTap: widget.onTap,
          child: ClipRRect(
            borderRadius: BorderRadius.circular(Rd.code),
            child: Container(
              width: w,
              height: h,
              color: c.fill,
              child: t == null
                  ? null
                  : Image.memory(
                      t.bytes,
                      fit: BoxFit.cover,
                      gaplessPlayback: true,
                    ),
            ),
          ),
        );
      },
    );
  }
}

/// 自己的话左边至少空出的宽度，和别人的话错开（同对话页的自己的话）。
const double _ownIndent = 48;
