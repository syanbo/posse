import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'api.dart';
import 'models.dart';

/// 表单里的一项：名字在上面一行，输入在下面。不用浮动标签：弹框、页面顶上的浮动标签会被裁掉一半。
class FormBlock extends StatelessWidget {
  const FormBlock({
    super.key,
    required this.label,
    required this.child,
    this.padding = const EdgeInsets.fromLTRB(16, 0, 16, 16),
  });
  final String label;
  final Widget child;
  final EdgeInsets padding;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Padding(
      padding: padding,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 0, 4, 6),
            child: Text(
              label,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: c.muted,
              ),
            ),
          ),
          child,
        ],
      ),
    );
  }
}

/// 只有几个文字框的小表单（新建、编辑讨论，用邀请加入）。成员的表单是整页，见 member_form.dart。
Future<DiscJson?> discussionForm(
  BuildContext context,
  DiscussionApi api,
  String title,
  String method,
  DiscJson params,
  Map<String, String> fields, {
  bool workspace = false,
  String? confirm,
}) => showPosseDialog<DiscJson>(
  context: context,
  builder: (_) => _DiscussionForm(
    api: api,
    title: title,
    method: method,
    params: params,
    fields: fields,
    workspace: workspace,
    confirm: confirm,
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
    this.confirm,
  });
  final DiscussionApi api;
  final String title, method;
  final String? confirm;
  final DiscJson params;
  final Map<String, String> fields;
  final bool workspace;
  @override
  State<_DiscussionForm> createState() => _DiscussionFormState();
}

class _DiscussionFormState extends State<_DiscussionForm> {
  late final fields = {
    for (final key in widget.fields.keys)
      key: TextEditingController(text: discText(widget.params, key)),
  };
  late String workspace = discText(widget.params, 'workspace');
  bool busy = false;
  DiscJson? request;
  Object? error;
  bool uncertain = false;
  List<WorktreeRow> get rows => widget.api.model.worktrees;

  @override
  void initState() {
    super.initState();
    if (workspace.isEmpty && rows.isNotEmpty) workspace = rows.first.id;
  }

  @override
  void dispose() {
    for (final c in fields.values) {
      c.dispose();
    }
    super.dispose();
  }

  /// 第一项（名字、邀请）必填。
  bool get ready =>
      !busy && (fields.isEmpty || fields.values.first.text.trim().isNotEmpty);

  Future<void> save() async {
    if (!ready) return;
    setState(() {
      busy = true;
      error = null;
    });
    request ??= {
      ...widget.params,
      for (final e in fields.entries) e.key: e.value.text.trim(),
      if (widget.workspace) 'workspace': workspace,
      'clientId': discRequestId(),
    };
    try {
      final result = await widget.api.call(widget.method, request!);
      if (mounted) Navigator.pop(context, result);
    } catch (e) {
      final unknown = discResultUnknown(e);
      if (mounted) {
        setState(() {
          error = e;
          busy = false;
          uncertain = unknown;
          if (!unknown) request = null;
        });
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final locked = request != null || busy;
    const flush = EdgeInsets.only(bottom: 14);
    return PopScope(
      canPop: !busy,
      child: AlertDialog(
        title: Text(widget.title),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (i, e) in fields.entries.indexed)
                FormBlock(
                  label: widget.fields[e.key]!,
                  padding: flush,
                  child: TextField(
                    controller: e.value,
                    enabled: !locked,
                    autofocus: i == 0,
                    minLines: 1,
                    maxLines: e.key == 'description' ? 4 : 1,
                    onChanged: (_) => setState(() {}),
                  ),
                ),
              if (widget.workspace)
                FormBlock(
                  label: l.discWorkspace,
                  padding: flush,
                  child: DropdownButtonFormField<String>(
                    key: ValueKey(workspace),
                    initialValue: rows.any((r) => r.id == workspace)
                        ? workspace
                        : null,
                    isExpanded: true,
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
                ),
              if (error != null || uncertain)
                Text(
                  [
                    if (error != null) friendlyError(error!, l),
                    if (uncertain) l.discUnknown,
                  ].join('\n'),
                  style: TextStyle(
                    fontSize: Fs.small,
                    height: Lh.body,
                    color: error != null ? c.danger : c.muted,
                  ),
                ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: busy ? null : () => Navigator.pop(context),
            child: Text(l.discCancel),
          ),
          FilledButton(
            onPressed: ready ? save : null,
            child: Text(
              error != null ? l.discRetry : widget.confirm ?? l.discSave,
            ),
          ),
        ],
      ),
    );
  }
}
