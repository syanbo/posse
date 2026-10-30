import 'dart:convert';

import 'package:posse_quic/posse_quic.dart';
import 'package:test/test.dart';

void main() {
  test('renders a diagram through the Go library', () async {
    final out =
        jsonDecode(
              await renderMermaid(
                jsonEncode({
                  'source': 'flowchart LR\nA[常驻进程] --> B[终端]',
                  'style': {'bg': '#141413', 'fg': '#ecebe8'},
                  'key': 'test',
                  'base': 16,
                  'advances': {'r': {}, 'b': {}, 'm': {}},
                }),
              ),
            )
            as Map<String, dynamic>;
    expect(out['err'], isNull);
    expect(out['svg'], startsWith('<svg'));
    expect(out['w'], greaterThan(0));
    final texts = [for (final l in out['labels'] as List) l['t']];
    expect(texts, containsAll(['常驻进程', '终端']));
  });

  test('reports a syntax error', () async {
    final out =
        jsonDecode(
              await renderMermaid(
                jsonEncode({'source': 'sequenceDiagram\nA->>B hi'}),
              ),
            )
            as Map<String, dynamic>;
    expect(out['err'], contains('line 2'));
  });
}
