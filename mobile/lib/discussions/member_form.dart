import 'dart:async';
import 'dart:math';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../usage/usage.dart' show agentTitle;
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'api.dart';
import 'forms.dart';
import 'models.dart';

/// 成员页上点了的操作：保存成功，或编辑时下面的打开终端、继续、重启、移除（交回讨论页去做）。
enum MemberOutcome { saved, open, resume, restart, remove }

/// 添加或编辑自己的 Agent：整页表单，和桌面的成员表单同一个顺序。
/// [member] 为空是添加；[people] 不为空（协作讨论）时有「谁能 @ 它」。
Future<MemberOutcome?> openMemberPage(
  BuildContext context, {
  required DiscussionApi api,
  required String discussion,
  DiscJson? member,
  required String workspace,
  String? project,
  List<DiscJson>? people,
  List<DiscJson> members = const [],
  bool live = false,
  bool resumable = false,
}) => Navigator.push<MemberOutcome>(
  context,
  MaterialPageRoute(
    builder: (_) => MemberPage(
      api: api,
      discussion: discussion,
      member: member,
      workspace: workspace,
      project: project,
      people: people,
      members: members,
      live: live,
      resumable: resumable,
    ),
  ),
);

class MemberPage extends StatefulWidget {
  const MemberPage({
    super.key,
    required this.api,
    required this.discussion,
    this.member,
    required this.workspace,
    this.project,
    this.people,
    this.members = const [],
    this.live = false,
    this.resumable = false,
  });
  final DiscussionApi api;
  final String discussion, workspace;
  final DiscJson? member;
  final String? project;
  final List<DiscJson>? people;
  final List<DiscJson> members;
  final bool live, resumable;
  @override
  State<MemberPage> createState() => _MemberPageState();
}

class _MemberPageState extends State<MemberPage> {
  DiscJson get m => widget.member ?? {};
  bool get adding => widget.member == null;
  late final name = TextEditingController(text: discText(m, 'name'));
  late final role = TextEditingController(text: discText(m, 'role'));
  late final model = TextEditingController(text: discText(m, 'model'));
  late final effort = TextEditingController(text: discText(m, 'effort'));
  final worktree = TextEditingController(text: _worktreeName());
  final base = TextEditingController();
  late String workspace = widget.workspace;
  late String agent = discText(m, 'agent');

  /// 0 用已有工作区，1 新建 worktree。
  int mode = 0;
  bool applyNow = false;

  /// 添加时名字跟着 Agent 变（Claude、Claude 2），用户改过就不再动。
  bool named = false;
  late int access = switch (discText(m, 'access')) {
    '' => 0,
    'all' => 1,
    _ => 2,
  };
  late final picked = {
    for (final fp in discText(m, 'access').split(','))
      if (fp.isNotEmpty && fp != 'all') fp,
  };
  bool busy = false;

  /// 发出去还没有确定结果的请求：结果未知时原样重发，不换请求 ID。
  DiscJson? request;
  Object? error;
  DiscJson errorData = {};
  bool uncertain = false;

  /// 添加的请求存在手机上：结果未知时关掉页面，再开还是这一份。
  DiscussionStorage? get storage => adding
      ? DiscussionStorage(
          widget.api.computer ?? '',
          '${widget.discussion}/member.add',
        )
      : null;

  List<WorktreeRow> get rows => widget.api.model.worktrees
      .where(
        (row) =>
            widget.project == null || row.id.startsWith('${widget.project}::'),
      )
      .toList();

  List<String> get agents => [
    ...widget.api.model.agents,
    if (agent.isNotEmpty && !widget.api.model.agents.contains(agent)) agent,
  ];

  /// 只有 git 项目能新建 worktree：看选中的（或第一个）工作区有没有分支。
  bool get git {
    final row = rows.where((r) => r.id == workspace).firstOrNull;
    return row != null && (row.branch.isNotEmpty || row.isWorktree);
  }

  static String _worktreeName() {
    final r = Random.secure();
    const abc = 'abcdefghijklmnopqrstuvwxyz0123456789';
    return 'discuss-${List.generate(4, (_) => abc[r.nextInt(abc.length)]).join()}';
  }

  String defaultName(String a) {
    final n = widget.members.where((x) => x['agent'] == a).length + 1;
    return n == 1 ? agentTitle(a) : '${agentTitle(a)} $n';
  }

  @override
  void initState() {
    super.initState();
    // 编辑时手机上的工作区列表可能还没刷新到成员的那个：不能悄悄换成第一个，只在空的时候补
    if (workspace.isEmpty && rows.isNotEmpty) workspace = rows.first.id;
    if (agent.isEmpty && agents.isNotEmpty) agent = agents.first;
    if (adding && name.text.isEmpty && agent.isNotEmpty) {
      name.text = defaultName(agent);
    }
    name.addListener(() {
      if (name.text != defaultName(agent)) named = true;
    });
    if (storage != null) {
      busy = true;
      unawaited(restore());
    }
  }

  Future<void> restore() async {
    final saved = await storage!.read();
    if (!mounted) return;
    setState(() {
      if (saved['request'] is Map) {
        final r = discMap(saved['request']);
        request = r;
        name.text = discText(r, 'name');
        role.text = discText(r, 'role');
        model.text = discText(r, 'model');
        effort.text = discText(r, 'effort');
        worktree.text = discText(r, 'worktree');
        base.text = discText(r, 'base');
        mode = worktree.text.isEmpty ? 0 : 1;
        if (mode == 0) worktree.text = _worktreeName();
        workspace = discText(r, 'workspace');
        agent = discText(r, 'agent');
        final was = discText(r, 'access');
        access = was.isEmpty
            ? 0
            : was == 'all'
            ? 1
            : 2;
        picked
          ..clear()
          ..addAll(was.split(',').where((fp) => fp.isNotEmpty && fp != 'all'));
        uncertain = true;
        errorData = discMap(saved['data']);
      }
      busy = false;
    });
  }

  @override
  void dispose() {
    for (final c in [name, role, model, effort, worktree, base]) {
      c.dispose();
    }
    super.dispose();
  }

  bool get ready =>
      !busy &&
      agent.isNotEmpty &&
      name.text.trim().isNotEmpty &&
      (mode == 0 || !git || worktree.text.trim().isNotEmpty) &&
      (access != 2 || picked.isNotEmpty);

  Future<void> save() async {
    if (!ready) return;
    setState(() {
      busy = true;
      error = null;
    });
    final newTree = mode == 1 && git;
    request ??= {
      'discussion': widget.discussion,
      if (!adding) 'member': m['id'],
      'name': name.text.trim(),
      'role': role.text.trim(),
      'agent': agent,
      'model': model.text.trim(),
      'effort': effort.text.trim(),
      'workspace': workspace,
      if (newTree) 'worktree': worktree.text.trim(),
      if (newTree) 'base': base.text.trim(),
      if (!adding) 'applyNow': applyNow,
      if (widget.people != null)
        'access': switch (access) {
          1 => 'all',
          2 => picked.join(','),
          _ => '',
        },
      'clientId': discRequestId(),
    };
    try {
      await storage?.write({'request': request, 'data': errorData});
      await widget.api.call(adding ? 'member.add' : 'member.update', request!);
      await storage?.write({});
      if (mounted) Navigator.pop(context, MemberOutcome.saved);
    } catch (e) {
      final data = e is RpcException ? discMap(e.data) : <String, Object?>{};
      final unknown =
          e is! RpcException ||
          ['closed', 'timeout', 'internal_error'].contains(e.code) ||
          data['unknown'] == true ||
          discText(data, 'worktree').isNotEmpty;
      await storage?.write(unknown ? {'request': request, 'data': data} : {});
      if (mounted) {
        setState(() {
          error = e;
          busy = false;
          uncertain = unknown;
          errorData = data;
          if (!unknown) request = null;
        });
      }
    }
  }

  Future<void> startOver() async {
    final l = context.l10n;
    if (!await showPosseConfirm(
      context,
      title: l.discNewRequest,
      body: l.discNewRequestWarning,
      confirm: l.discNewRequest,
      tone: ConfirmTone.warning,
    )) {
      return;
    }
    await storage?.write({});
    if (!mounted) return;
    setState(() {
      request = null;
      uncertain = false;
      error = null;
      errorData = {};
      worktree.text = _worktreeName();
    });
  }

  Widget field(TextEditingController c, {String? hint, int lines = 1}) =>
      TextField(
        controller: c,
        enabled: request == null && !busy,
        minLines: 1,
        maxLines: lines,
        onChanged: (_) => setState(() {}),
        decoration: InputDecoration(hintText: hint),
      );

  Widget workspacePicker(bool locked) => DropdownButtonFormField<String>(
    key: ValueKey('ws/$workspace'),
    initialValue: rows.any((r) => r.id == workspace) ? workspace : null,
    isExpanded: true,
    items: [
      for (final row in rows)
        DropdownMenuItem(
          value: row.id,
          child: Text(worktreeLabel(row), overflow: TextOverflow.ellipsis),
        ),
    ],
    onChanged: locked ? null : (v) => setState(() => workspace = v ?? ''),
  );

  Widget accessPicker(AppLocalizations l, bool locked) {
    final c = PosseColors.of(context);
    final people = widget.people!;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        IgnorePointer(
          ignoring: locked,
          child: Segmented(
            labels: [l.discAccessOnlyMe, l.discAccessAll, l.discAccessSome],
            selected: access,
            onChanged: (i) => setState(() => access = i),
          ),
        ),
        if (access == 2) ...[
          const SizedBox(height: 8),
          if (people.isEmpty)
            Text(
              l.discAccessNobody,
              style: TextStyle(fontSize: Fs.small, color: c.muted),
            )
          else
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                for (final p in people)
                  FilterChip(
                    label: Text(discText(p, 'name')),
                    showCheckmark: false,
                    side: BorderSide.none,
                    backgroundColor: c.fill,
                    selectedColor: c.tint,
                    shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(Rd.button),
                    ),
                    labelStyle: TextStyle(
                      fontSize: Fs.body,
                      color: picked.contains(p['fp']) ? c.accent : c.text,
                    ),
                    selected: picked.contains(p['fp']),
                    onSelected: locked
                        ? null
                        : (on) => setState(() {
                            final fp = discText(p, 'fp');
                            on ? picked.add(fp) : picked.remove(fp);
                          }),
                  ),
              ],
            ),
        ],
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final locked = request != null || busy;
    final list = agents;
    final mirror = widget.project == null;
    final hasWorker = discText(m, 'taskId').isNotEmpty;
    final ops = <Widget>[
      if (widget.live)
        RowTile(
          title: Text(l.discOpenWorker),
          onTap: busy ? null : () => Navigator.pop(context, MemberOutcome.open),
        ),
      if (widget.resumable)
        RowTile(
          title: Text(l.discResume),
          chevron: false,
          onTap: busy
              ? null
              : () => Navigator.pop(context, MemberOutcome.resume),
        ),
      RowTile(
        title: Text(l.discRestart),
        chevron: false,
        onTap: busy
            ? null
            : () => Navigator.pop(context, MemberOutcome.restart),
      ),
      RowTile(
        title: Text(l.discRemove, style: TextStyle(color: c.danger)),
        chevron: false,
        onTap: busy ? null : () => Navigator.pop(context, MemberOutcome.remove),
      ),
    ];
    return PopScope(
      canPop: !busy,
      child: Scaffold(
        appBar: AppBar(
          title: Text(adding ? l.discAddMember : l.discEditMember),
          actions: [
            if (busy)
              const Padding(
                padding: EdgeInsets.symmetric(horizontal: 20),
                child: HexSpinner(size: Ic.medium),
              )
            else
              Padding(
                padding: const EdgeInsets.only(right: 8),
                child: TextButton(
                  // 顶栏里的文字按钮不要底色，和返回、图标按钮一样平
                  style: TextButton.styleFrom(
                    backgroundColor: Colors.transparent,
                    foregroundColor: PosseColors.of(context).accent,
                  ),
                  onPressed: ready ? save : null,
                  child: Text(
                    error != null
                        ? l.discRetry
                        : adding
                        ? l.discAdd
                        : l.discSave,
                  ),
                ),
              ),
          ],
        ),
        body: SideSafe(
          child: ListView(
            padding: const EdgeInsets.only(top: 8, bottom: 32),
            children: [
              if (error != null)
                NoticeBar(
                  [
                    friendlyError(error!, l),
                    if (uncertain) l.discUnknown,
                    if (discText(errorData, 'worktree').isNotEmpty)
                      discText(errorData, 'worktree'),
                  ].join('\n'),
                )
              else if (uncertain)
                NoticeBar(l.discUnknown),
              if (uncertain)
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
                  child: Align(
                    alignment: Alignment.centerLeft,
                    child: TextButton(
                      onPressed: busy ? null : startOver,
                      child: Text(l.discNewRequest),
                    ),
                  ),
                ),
              FormBlock(
                label: l.discAgent,
                child: list.isEmpty
                    ? Text(
                        l.discNoAgents,
                        style: TextStyle(fontSize: Fs.body, color: c.muted),
                      )
                    : list.length <= 4
                    ? IgnorePointer(
                        ignoring: locked,
                        child: Segmented(
                          labels: [for (final a in list) agentTitle(a)],
                          selected: max(0, list.indexOf(agent)),
                          onChanged: (i) => setState(() {
                            agent = list[i];
                            if (adding && !named) {
                              name.text = defaultName(agent);
                            }
                          }),
                        ),
                      )
                    : DropdownButtonFormField<String>(
                        key: ValueKey('agent/$agent'),
                        initialValue: agent,
                        items: [
                          for (final a in list)
                            DropdownMenuItem(
                              value: a,
                              child: Text(agentTitle(a)),
                            ),
                        ],
                        onChanged: locked
                            ? null
                            : (v) => setState(() {
                                agent = v ?? agent;
                                if (adding && !named) {
                                  name.text = defaultName(agent);
                                }
                              }),
                      ),
              ),
              FormBlock(label: l.discName, child: field(name)),
              FormBlock(
                label: l.discRole,
                child: field(role, hint: l.discRoleHint, lines: 5),
              ),
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 16),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(
                      child: FormBlock(
                        label: l.discModelLabel,
                        padding: EdgeInsets.zero,
                        child: field(model, hint: l.discDefault),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: FormBlock(
                        label: l.discEffortLabel,
                        padding: EdgeInsets.zero,
                        child: field(effort, hint: l.discDefault),
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 16),
              FormBlock(
                label: l.discWorkDir,
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    if (git) ...[
                      IgnorePointer(
                        ignoring: locked,
                        child: Segmented(
                          labels: [
                            l.discExistingWorkspace,
                            l.discNewWorktreeMode,
                          ],
                          selected: mode,
                          onChanged: (i) => setState(() => mode = i),
                        ),
                      ),
                      const SizedBox(height: 8),
                    ],
                    // 加入的讨论没有本机项目：新建 worktree 也要选在哪个项目上建
                    if (mode == 0 || !git || mirror) workspacePicker(locked),
                    if (mode == 1 && git) ...[
                      FormBlock(
                        label: l.discWorktreeName,
                        padding: EdgeInsets.only(top: mirror ? 12 : 4),
                        child: field(worktree),
                      ),
                      FormBlock(
                        label: l.discBase,
                        padding: const EdgeInsets.only(top: 12),
                        child: field(base, hint: l.discDefault),
                      ),
                    ],
                  ],
                ),
              ),
              if (widget.people != null)
                FormBlock(
                  label: l.discAccessLabel,
                  child: accessPicker(l, locked),
                ),
              if (!adding && hasWorker)
                Section(
                  margin: const EdgeInsets.fromLTRB(16, 4, 16, 20),
                  children: [
                    RowTile(
                      title: Text(l.discApplyNow),
                      chevron: false,
                      trailing: PosseSwitch(
                        value: applyNow,
                        onChanged: (v) {
                          if (!locked) setState(() => applyNow = v);
                        },
                      ),
                    ),
                  ],
                ),
              if (!adding) ...[
                const SizedBox(height: 12),
                Section(children: ops),
              ],
            ],
          ),
        ),
      ),
    );
  }
}
