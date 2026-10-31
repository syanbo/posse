import 'package:flutter/material.dart';

import 'prompt.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'chat_style.dart';

class ApprovalCard extends StatelessWidget {
  const ApprovalCard({
    super.key,
    required this.approval,
    required this.busy,
    required this.onDecision,
  });

  final Approval approval;
  final bool busy;
  final void Function(String key) onDecision;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
      decoration: askCard(c),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const StatusHex(state: AgentState.waiting, size: Ic.medium),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  '允许 ${approval.tool}？',
                  style: TextStyle(
                    fontSize: Fs.title,
                    fontWeight: Fw.bold,
                    color: c.text,
                  ),
                ),
              ),
            ],
          ),
          if (approval.summary.isNotEmpty) ...[
            const SizedBox(height: 8),
            Container(
              width: double.infinity,
              constraints: const BoxConstraints(maxHeight: 140),
              padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
              decoration: BoxDecoration(
                color: c.code,
                borderRadius: BorderRadius.circular(Rd.button),
              ),
              child: SingleChildScrollView(
                child: Text(
                  approval.summary,
                  style: mono(Fs.small, color: c.muted, height: Lh.code),
                ),
              ),
            ),
          ],
          const SizedBox(height: 12),
          Row(
            children: [
              Expanded(
                child: TextButton(
                  onPressed: busy ? null : () => onDecision(denyKey),
                  child: const Text('拒绝'),
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: FilledButton(
                  onPressed: busy ? null : () => onDecision(allowKey),
                  child: const Text('允许'),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class QuestionCard extends StatefulWidget {
  const QuestionCard({
    super.key,
    required this.questions,
    required this.onSubmit,
  });

  final List<AskQuestion> questions;
  final void Function(List<String> lines) onSubmit;

  @override
  State<QuestionCard> createState() => _QuestionCardState();
}

class _QuestionCardState extends State<QuestionCard> {
  final _selected = <int, Set<String>>{};
  final _other = <int, TextEditingController>{};

  @override
  void dispose() {
    for (final controller in _other.values) {
      controller.dispose();
    }
    super.dispose();
  }

  TextEditingController _otherOf(int index) =>
      _other.putIfAbsent(index, TextEditingController.new);

  bool get _ready {
    for (var i = 0; i < widget.questions.length; i++) {
      final other = _other[i]?.text.trim() ?? '';
      if ((_selected[i]?.isNotEmpty ?? false) || other.isNotEmpty) continue;
      return false;
    }
    return widget.questions.isNotEmpty;
  }

  void _submit() {
    final lines = <String>[];
    for (var i = 0; i < widget.questions.length; i++) {
      final other = _other[i]?.text.trim() ?? '';
      if (other.isNotEmpty) {
        lines.add(other);
        continue;
      }
      lines.add(formatAnswer(widget.questions[i], [...?_selected[i]]));
    }
    widget.onSubmit(lines);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final questions = widget.questions;
    return Container(
      margin: const EdgeInsets.fromLTRB(12, 0, 12, 8),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      constraints: BoxConstraints(
        maxHeight: MediaQuery.sizeOf(context).height * 0.5,
      ),
      decoration: askCard(c),
      // 题目多时只滚题目，提交按钮一直在卡片底部
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Flexible(
            child: SingleChildScrollView(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (var i = 0; i < questions.length; i++) ...[
                    if (questions[i].header != null)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 2),
                        child: Text(
                          questions[i].header!,
                          style: TextStyle(color: c.accent, fontSize: Fs.small),
                        ),
                      ),
                    Text(
                      questions[i].question,
                      style: TextStyle(
                        fontSize: Fs.body,
                        fontWeight: Fw.bold,
                        color: c.text,
                      ),
                    ),
                    const SizedBox(height: 8),
                    Wrap(
                      spacing: 6,
                      runSpacing: 6,
                      children: [
                        for (final option in questions[i].options)
                          FilterChip(
                            label: Text(option.label),
                            showCheckmark: false,
                            side: BorderSide.none,
                            backgroundColor: c.fill,
                            selectedColor: c.tint,
                            labelStyle: TextStyle(
                              color:
                                  (_selected[i]?.contains(option.label) ??
                                      false)
                                  ? c.accent
                                  : c.text,
                            ),
                            shape: RoundedRectangleBorder(
                              borderRadius: BorderRadius.circular(Rd.button),
                            ),
                            selected:
                                _selected[i]?.contains(option.label) ?? false,
                            onSelected: (selected) => setState(() {
                              final set = _selected.putIfAbsent(i, () => {});
                              if (questions[i].multiSelect) {
                                selected
                                    ? set.add(option.label)
                                    : set.remove(option.label);
                              } else {
                                set
                                  ..clear()
                                  ..add(option.label);
                                _otherOf(i).clear();
                              }
                            }),
                          ),
                      ],
                    ),
                    const SizedBox(height: 8),
                    TextField(
                      controller: _otherOf(i),
                      decoration: const InputDecoration(
                        hintText: '其他回答',
                        isDense: true,
                      ),
                      onChanged: (_) => setState(() {}),
                    ),
                    const SizedBox(height: 12),
                  ],
                ],
              ),
            ),
          ),
          Align(
            alignment: Alignment.centerRight,
            child: FilledButton(
              onPressed: _ready ? _submit : null,
              child: const Text('提交'),
            ),
          ),
        ],
      ),
    );
  }
}
