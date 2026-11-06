part of 'room.dart';

/// 打 @ 时弹出的一项：能用的 Agent（作为请求执行）、用不了的别人的 Agent（提醒它的拥有者）、群里的人（只提醒）。
class _AtCandidate {
  const _AtCandidate({this.member, this.person, this.can = false});
  final DiscJson? member, person;
  final bool can;
  String get name => discText(member ?? person!, 'name');
}

/// 光标前正在打的 @：返回 @ 的位置和后面打的字；没有返回 -1。
/// @ 前面紧挨着英文字母、数字时（邮箱）不算；@ 和光标之间有空白就不算。和桌面的 atToken 一致。
(int, String) discAtToken(String text, int caret) {
  final r = text.runes.toList();
  caret = caret.clamp(0, r.length);
  for (var i = caret - 1; i >= 0; i--) {
    final ch = String.fromCharCode(r[i]);
    if (ch.trim().isEmpty) return (-1, '');
    if (ch == '@' || ch == '\uFF20') {
      if (i > 0 &&
          RegExp(r'[A-Za-z0-9]').hasMatch(String.fromCharCode(r[i - 1]))) {
        return (-1, '');
      }
      return (i, String.fromCharCodes(r.sublist(i + 1, caret)));
    }
  }
  return (-1, '');
}

extension _ComposerViews on _DiscussionRoomState {
  String get selfFp => discText(state.collab, 'self');

  List<DiscJson> get activeMembers =>
      state.members.where((m) => m['removedAt'] == null).toList();

  List<DiscJson> get activePeople =>
      discList(state.collab['people'])
          .where((p) => p['state'] == 'active')
          .toList();

  /// 能 @ 这个 Agent 让它执行：自己的，或拥有者放开给自己的。
  bool canUse(DiscJson m) {
    if (discMine(m, widget.self)) return true;
    final access = discText(m, 'access');
    return access == 'all' ||
        selfFp.isNotEmpty && access.split(',').contains(selfFp);
  }

  String personName(String fp) {
    final p =
        activePeople.where((p) => p['fp'] == fp).firstOrNull ??
        discList(state.collab['people'])
            .where((p) => p['fp'] == fp)
            .firstOrNull;
    return p == null ? '' : discText(p, 'name');
  }

  int get caret {
    final s = input.selection;
    final text = input.text;
    // selection 按 UTF-16 计，@ 的位置按字符计
    final end = s.isValid ? s.baseOffset.clamp(0, text.length) : text.length;
    return text.substring(0, end).runes.length;
  }

  List<_AtCandidate> atCandidates(String query) {
    final q = query.toLowerCase();
    bool match(String name) => q.isEmpty || name.toLowerCase().contains(q);
    final can = <_AtCandidate>[],
        others = <_AtCandidate>[],
        people = <_AtCandidate>[];
    for (final m in activeMembers) {
      if (!match(discText(m, 'name'))) continue;
      if (canUse(m)) {
        can.add(_AtCandidate(member: m, can: true));
      } else if (personName(discText(m, 'owner')).isNotEmpty) {
        others.add(_AtCandidate(member: m));
      }
    }
    for (final p in activePeople) {
      if (p['fp'] != selfFp && match(discText(p, 'name'))) {
        people.add(_AtCandidate(person: p));
      }
    }
    return [...can, ...others, ...people];
  }

  /// 选中一项：能用的 Agent、人从正文里去掉 @ 和打的字，换成接收者；
  /// 用不了的别人的 Agent 补全名字留在正文里，提醒它的拥有者。
  void pickAt(int at, String query, _AtCandidate x) {
    final r = input.text.runes.toList();
    final end = (at + 1 + query.runes.length).clamp(0, r.length);
    var insert = '';
    if (x.member != null && x.can) {
      target = discText(x.member!, 'id');
      mention = '';
    } else if (x.member != null) {
      mention = discText(x.member!, 'owner');
      target = '';
      insert = '@${x.name} ';
    } else {
      mention = discText(x.person!, 'fp');
      target = '';
    }
    final before = String.fromCharCodes(r.sublist(0, at)) + insert;
    input.value = TextEditingValue(
      text: before + String.fromCharCodes(r.sublist(end)),
      selection: TextSelection.collapsed(offset: before.length),
    );
    update(() {});
    scheduleSave();
  }

  Widget atPicker() {
    if (!writable || pending != null) return const SizedBox.shrink();
    final (at, query) = discAtToken(input.text, caret);
    if (at < 0) return const SizedBox.shrink();
    final list = atCandidates(query);
    if (list.isEmpty) return const SizedBox.shrink();
    final c = PosseColors.of(context);
    final l = context.l10n;
    return Container(
      constraints: const BoxConstraints(maxHeight: 240),
      margin: const EdgeInsets.fromLTRB(10, 0, 10, 6),
      decoration: BoxDecoration(
        color: c.raised,
        borderRadius: BorderRadius.circular(Rd.card),
        border: Border.all(color: c.line),
        boxShadow: [
          BoxShadow(
            color: c.shadow,
            blurRadius: 16,
            offset: const Offset(0, 4),
          ),
        ],
      ),
      clipBehavior: Clip.antiAlias,
      child: ListView(
        shrinkWrap: true,
        padding: const EdgeInsets.symmetric(vertical: 4),
        children: [
          for (final x in list)
            InkWell(
              onTap: () => pickAt(at, query, x),
              highlightColor: c.wash,
              child: Padding(
                padding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 8,
                ),
                child: Row(
                  children: [
                    x.member != null
                        ? AgentTile(
                            agent: discText(x.member!, 'agent'),
                            size: Ic.button,
                          )
                        : initial(x.name, Ic.button),
                    const SizedBox(width: 10),
                    Expanded(
                      child: Text(
                        x.name,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: Fs.body, color: c.text),
                      ),
                    ),
                    if (x.member != null && !x.can)
                      Text(
                        l.discAtOthersAgent(
                          personName(discText(x.member!, 'owner')),
                        ),
                        style: TextStyle(fontSize: Fs.small, color: c.muted),
                      ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }

  Widget chip(
    String text,
    VoidCallback? onRemove, {
    Color? color,
    VoidCallback? onTap,
  }) {
    final c = PosseColors.of(context);
    return GestureDetector(
      onTap: onTap,
      child: Container(
        padding: EdgeInsets.fromLTRB(8, 4, onRemove == null ? 8 : 4, 4),
        decoration: BoxDecoration(
          color: color == null ? c.fill : c.soft(color),
          borderRadius: BorderRadius.circular(Rd.button),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 200),
              child: Text(
                text,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: Fs.small, color: color ?? c.text),
              ),
            ),
            if (onRemove != null) ...[
              const SizedBox(width: 2),
              GestureDetector(
                onTap: onRemove,
                child: Icon(
                  Icons.close_rounded,
                  size: Ic.inline,
                  color: c.faint,
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Widget composer() {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final locked = pending != null;
    if (!writable) {
      return SafeArea(
        top: false,
        child: Container(
          width: double.infinity,
          margin: const EdgeInsets.fromLTRB(12, 4, 12, 8),
          padding: const EdgeInsets.all(14),
          decoration: BoxDecoration(
            color: c.fill,
            borderRadius: BorderRadius.circular(Rd.composer),
          ),
          child: Text(
            unavailable,
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.muted,
            ),
          ),
        ),
      );
    }
    final targetRow = activeMembers.where((m) => m['id'] == target).firstOrNull;
    final quoteRow = state.messages.where((m) => m['id'] == quote).firstOrNull;
    final chips = [
      if (targetRow != null)
        chip(
          '@${discText(targetRow, 'name')}',
          locked ? null : () => update(() => target = ''),
          color: c.accent,
        ),
      if (mention.isNotEmpty)
        chip(
          '@${personName(mention).isEmpty ? mention : personName(mention)}',
          locked ? null : () => update(() => mention = ''),
          color: c.accent,
        ),
      if (quote.isNotEmpty)
        chip(
          '${l.discQuote} · ${quoteRow == null ? '' : plainSnippet(discText(quoteRow, 'body'))}',
          locked ? null : () => update(() => quote = ''),
          onTap: () => locate(quote),
        ),
      for (final a in attachments)
        chip(discText(a, 'name'), locked ? null : () => removeAttachment(a)),
    ];
    final ready =
        !sending &&
        !uploading &&
        (locked || input.text.trim().isNotEmpty || attachments.isNotEmpty);
    return SafeArea(
      top: false,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          atPicker(),
          if (locked)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 8, 2),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      l.discUnknown,
                      style: TextStyle(fontSize: Fs.small, color: c.muted),
                    ),
                  ),
                  TextButton(
                    onPressed: sending ? null : editDraft,
                    child: Text(l.discEditDraft),
                  ),
                ],
              ),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 2, 10, 6),
            child: Container(
              decoration: BoxDecoration(
                color: c.card,
                borderRadius: BorderRadius.circular(Rd.composer),
                border: Border.all(color: c.line),
              ),
              padding: const EdgeInsets.all(4),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (chips.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.fromLTRB(6, 4, 6, 2),
                      child: Wrap(spacing: 6, runSpacing: 6, children: chips),
                    ),
                  Row(
                    children: [
                      IconButton(
                        tooltip: l.discAddFile,
                        style: IconButton.styleFrom(
                          fixedSize: const Size.square(34),
                          minimumSize: const Size.square(34),
                          padding: EdgeInsets.zero,
                          tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        ),
                        onPressed: locked || uploading ? null : pickAttachment,
                        icon: Icon(
                          Icons.add_rounded,
                          size: Ic.button,
                          color: c.muted,
                        ),
                      ),
                      Expanded(
                        child: TextField(
                          controller: input,
                          enabled: !locked,
                          minLines: 1,
                          maxLines: 6,
                          textInputAction: TextInputAction.newline,
                          onChanged: (_) => update(() {}),
                          onTap: () => update(() {}),
                          style: TextStyle(fontSize: Fs.body, color: c.text),
                          decoration: InputDecoration(
                            hintText: l.discMessageHint,
                            filled: false,
                            border: InputBorder.none,
                            enabledBorder: InputBorder.none,
                            focusedBorder: InputBorder.none,
                            disabledBorder: InputBorder.none,
                            isDense: true,
                            contentPadding: const EdgeInsets.fromLTRB(
                              6,
                              6,
                              4,
                              6,
                            ),
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
                          tooltip: locked ? l.discRetry : l.discSend,
                          onPressed: ready ? send : null,
                          icon: sending
                              ? HexSpinner(color: c.muted)
                              : Icon(
                                  locked
                                      ? Icons.refresh_rounded
                                      : Icons.arrow_upward_rounded,
                                  size: Ic.medium,
                                  color: ready ? Colors.white : c.faint,
                                ),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }

  /// 加附件：照片或文件。
  Future<void> pickAttachment() async {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final photo = await showModalBottomSheet<bool>(
      context: context,
      showDragHandle: true,
      builder: (sheet) => SafeArea(
        child: Section(
          children: [
            RowTile(
              leading: Icon(Icons.photo_rounded, color: c.muted),
              title: Text(l.discAddPhoto),
              chevron: false,
              onTap: () => Navigator.pop(sheet, true),
            ),
            RowTile(
              leading: Icon(Icons.insert_drive_file_rounded, color: c.muted),
              title: Text(l.discAddFile),
              chevron: false,
              onTap: () => Navigator.pop(sheet, false),
            ),
          ],
        ),
      ),
    );
    if (photo != null && mounted) await attach(photo: photo);
  }
}
