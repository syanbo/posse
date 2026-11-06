import 'dart:async';

import 'package:flutter/material.dart';

import '../workspace/models.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import '../l10n/l10n.dart';
import '../ui/widgets.dart';
import '../workspace/workspace.dart';
import 'api.dart';
import 'models.dart';

Future<DiscJson?> discussionForm(
  BuildContext context,
  DiscussionApi api,
  String title,
  String method,
  DiscJson params,
  Map<String, String> fields, {
  bool workspace = false,
  bool agent = false,
  bool applyNow = false,
  String? project,
}) => showPosseDialog<DiscJson>(
  context: context,
  builder: (_) => _DiscussionForm(
    api: api,
    title: title,
    method: method,
    params: params,
    fields: fields,
    workspace: workspace,
    agent: agent,
    applyNow: applyNow,
    project: project,
  ),
);

class _DiscussionForm extends StatefulWidget {
  const _DiscussionForm({
    required this.api,
    required this.title,
    required this.method,
    required this.params,
    required this.fields,
    required this.workspace,
    required this.agent,
    required this.applyNow,
    this.project,
  });
  final DiscussionApi api;
  final String title, method;
  final String? project;
  final DiscJson params;
  final Map<String, String> fields;
  final bool workspace, agent, applyNow;
  @override
  State<_DiscussionForm> createState() => _DiscussionFormState();
}

class _DiscussionFormState extends State<_DiscussionForm> {
  late final fields = {
    for (final key in widget.fields.keys)
      key: TextEditingController(text: discText(widget.params, key)),
  };
  late String workspace = discText(widget.params, 'workspace');
  late String agent = discText(widget.params, 'agent');
  bool applyNow = false;
  bool busy = false;
  DiscJson? request;
  Object? error;
  DiscJson errorData = {};
  bool uncertain = false;
  DiscussionStorage? get storage => widget.method == 'member.add'
      ? DiscussionStorage(
          widget.api.computer ?? '',
          '${discText(widget.params, 'discussion')}/member.add',
        )
      : null;
  List<WorktreeRow> get rows => widget.api.model.worktrees
      .where(
        (row) =>
            widget.project == null || row.id.startsWith('${widget.project}::'),
      )
      .toList();

  @override
  void initState() {
    super.initState();

    if (workspace.isEmpty && rows.isNotEmpty) workspace = rows.first.id;
    if (agent.isEmpty && widget.api.model.agents.isNotEmpty) {
      agent = widget.api.model.agents.first;
    }
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
        request = discMap(saved['request']);
        for (final e in fields.entries) {
          e.value.text = discText(request!, e.key);
        }
        workspace = discText(request!, 'workspace');
        agent = discText(request!, 'agent');
        applyNow = request!['applyNow'] == true;
        uncertain = true;
        errorData = discMap(saved['data']);
      }
      busy = false;
    });
  }

  @override
  void dispose() {
    for (final c in fields.values) {
      c.dispose();
    }
    super.dispose();
  }

  Future<void> save() async {
    setState(() {
      busy = true;
      error = null;
    });
    request ??= {
      ...widget.params,
      for (final e in fields.entries) e.key: e.value.text,
      if (widget.workspace) 'workspace': workspace,
      if (widget.agent) 'agent': agent,
      if (widget.applyNow) 'applyNow': applyNow,
      'clientId': discRequestId(),
    };
    try {
      await storage?.write({'request': request, 'data': errorData});
      final result = await widget.api.call(widget.method, request!);
      await storage?.write({});
      if (mounted) Navigator.pop(context, result);
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

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final locked = request != null || busy;
    final git = rows.any(
      (r) => r.id == workspace && (r.branch.isNotEmpty || r.isWorktree),
    );
    final agents = {...widget.api.model.agents, if (agent.isNotEmpty) agent};
    return PopScope(
      canPop: !busy,
      child: AlertDialog(
        title: Text(widget.title),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              for (final e in fields.entries)
                if (!['worktree', 'base'].contains(e.key) || git)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: TextField(
                      controller: e.value,
                      enabled: !locked,
                      minLines: 1,
                      maxLines:
                          e.key == 'role' ||
                              e.key == 'description' ||
                              e.key == 'body'
                          ? 5
                          : 1,
                      decoration: InputDecoration(
                        labelText: widget.fields[e.key],
                      ),
                    ),
                  ),
              if (widget.workspace)
                DropdownButtonFormField<String>(
                  key: ValueKey(workspace),
                  initialValue: rows.any((r) => r.id == workspace)
                      ? workspace
                      : null,
                  isExpanded: true,
                  decoration: InputDecoration(labelText: l.discWorkspace),
                  items: [
                    for (final row in rows)
                      DropdownMenuItem(
                        value: row.id,
                        child: Text(
                          worktreeLabel(row),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                  ],
                  onChanged: locked
                      ? null
                      : (v) => setState(() => workspace = v ?? ''),
                ),
              if (widget.agent)
                DropdownButtonFormField<String>(
                  key: ValueKey(agent),
                  initialValue: agent.isEmpty ? null : agent,
                  decoration: InputDecoration(labelText: l.discAgent),
                  items: [
                    for (final a in agents)
                      DropdownMenuItem(value: a, child: Text(a)),
                  ],
                  onChanged: locked
                      ? null
                      : (v) => setState(() => agent = v ?? ''),
                ),
              if (widget.applyNow)
                CheckboxListTile(
                  value: applyNow,
                  title: Text(l.discApplyNow),
                  onChanged: locked
                      ? null
                      : (v) => setState(() => applyNow = v ?? false),
                ),
              if (uncertain && error == null) Text(l.discUnknown),
              if (error != null)
                Padding(
                  padding: const EdgeInsets.only(top: 12),
                  child: Column(
                    children: [
                      Text(friendlyError(error!, l)),
                      const SizedBox(height: 8),
                      if (uncertain) Text(l.discUnknown),
                      if (discText(errorData, 'worktree').isNotEmpty)
                        Text(discText(errorData, 'worktree')),
                    ],
                  ),
                ),
            ],
          ),
        ),
        actions: [
          if (uncertain)
            TextButton(
              onPressed: busy
                  ? null
                  : () async {
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
                        fields['worktree']?.clear();
                      });
                    },
              child: Text(l.discNewRequest),
            ),

          TextButton(
            onPressed: busy ? null : () => Navigator.pop(context),
            child: Text(l.discCancel),
          ),
          FilledButton(
            onPressed: busy ? null : save,
            child: Text(error != null ? l.discRetry : l.discSave),
          ),
        ],
      ),
    );
  }
}
