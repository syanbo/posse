import 'dart:convert';

class Approval {
  const Approval(this.tool, this.summary);
  final String tool;
  final String summary;
}

class AskOption {
  const AskOption(this.label, [this.description]);
  final String label;
  final String? description;
}

class AskQuestion {
  const AskQuestion({
    required this.question,
    required this.options,
    this.header,
    this.multiSelect = false,
  });

  final String question;
  final String? header;
  final bool multiSelect;
  final List<AskOption> options;
}

class InteractivePrompt {
  const InteractivePrompt({this.approval, this.questions = const []});
  final Approval? approval;
  final List<AskQuestion> questions;
}

const questionSubmitDelay = Duration(milliseconds: 500);
const questionStep = Duration(milliseconds: 1000);

class AnswerTiming {
  const AnswerTiming(this.bodyAt, this.enterAt);
  final Duration bodyAt;
  final Duration enterAt;
}

AnswerTiming questionTiming(int index) {
  final bodyAt = questionStep * index;
  return AnswerTiming(bodyAt, bodyAt + questionSubmitDelay);
}

String formatAnswer(AskQuestion question, List<String> selected) {
  final labels = [for (final label in selected) label.trim()]
      .where((label) => label.isNotEmpty);
  if (question.multiSelect) return labels.join(', ');
  return labels.isEmpty ? '' : labels.first;
}

/// 允许是 `1`，拒绝是 Esc。这两种都是完整按键，不能再补回车。
const allowKey = '1';
const denyKey = '\x1b';

InteractivePrompt? parseInteractivePrompt(String? raw) {
  if (raw == null || raw.isEmpty) return null;
  try {
    final decoded = jsonDecode(raw);
    if (decoded is! Map) return null;
    final approval = decoded['approval'];
    if (approval is Map && approval['tool'] is String) {
      return InteractivePrompt(
        approval: Approval(
          approval['tool'] as String,
          approval['summary'] as String? ?? '',
        ),
      );
    }
    final rawQuestions = decoded['questions'];
    if (rawQuestions is! List) return null;
    final questions = <AskQuestion>[];
    for (final item in rawQuestions) {
      if (item is! Map || item['question'] is! String) continue;
      final options = <AskOption>[];
      final rawOptions = item['options'];
      if (rawOptions is List) {
        for (final option in rawOptions) {
          if (option is Map && option['label'] is String) {
            options.add(
              AskOption(
                option['label'] as String,
                option['description'] as String?,
              ),
            );
          }
        }
      }
      questions.add(
        AskQuestion(
          question: item['question'] as String,
          header: item['header'] as String?,
          multiSelect: item['multiSelect'] == true,
          options: options,
        ),
      );
    }
    if (questions.isEmpty) return null;
    return InteractivePrompt(questions: questions);
  } catch (_) {
    return null;
  }
}

Future<void> runAnswerSteps({
  required List<String> lines,
  required Future<void> Function(String line) writeBody,
  required Future<void> Function() writeEnter,
  Future<void> Function(Duration delay)? sleep,
}) async {
  final wait = sleep ?? Future<void>.delayed;
  var elapsed = Duration.zero;
  for (var i = 0; i < lines.length; i++) {
    final timing = questionTiming(i);
    if (timing.bodyAt > elapsed) {
      await wait(timing.bodyAt - elapsed);
      elapsed = timing.bodyAt;
    }
    await writeBody(lines[i]);
    if (timing.enterAt > elapsed) {
      await wait(timing.enterAt - elapsed);
      elapsed = timing.enterAt;
    }
    await writeEnter();
  }
}
