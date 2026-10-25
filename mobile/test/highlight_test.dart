import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/highlight.dart';

Tok? tokOf(HlLines lines, int line, String text) {
  for (final (t, tok) in lines[line]) {
    if (t.contains(text)) return tok;
  }
  throw StateError('"$text" not on line $line: ${lines[line]}');
}

void main() {
  test('language by file name and extension', () {
    expect(languageForPath('lib/main.dart'), 'dart');
    expect(languageForPath('a/b/Component.TSX'), 'typescript');
    expect(languageForPath('ios/Podfile'), 'ruby');
    expect(languageForPath('Dockerfile'), 'dockerfile');
    expect(languageForPath('notes.txt'), isNull);
    expect(languageForPath('LICENSE'), isNull);
  });

  test('fence languages accept highlight.js aliases', () {
    expect(languageForFence('ts'), 'ts');
    expect(languageForFence('sh'), 'bash');
    expect(languageForFence('python title="x"'), 'python');
    expect(languageForFence('nope'), isNull);
    expect(languageForFence(''), isNull);
  });

  test('lines match the source and block comments span lines', () {
    const code = 'void main() {\n  /* one\n  two */\n  final s = "a\$b";\n}';
    final hl = highlightSync(code, 'dart')!;
    expect(hl.length, code.split('\n').length);
    for (var i = 0; i < hl.length; i++) {
      expect(hl[i].map((s) => s.$1).join(), code.split('\n')[i]);
    }
    expect(tokOf(hl, 0, 'void'), Tok.keyword);
    expect(tokOf(hl, 2, 'two'), Tok.comment);
    expect(tokOf(hl, 3, 'final'), Tok.keyword);
    expect(tokOf(hl, 3, '"a'), Tok.string);
  });

  test('unknown or oversized input is not highlighted', () {
    expect(highlightSync('x', null), isNull);
    expect(highlightSync('x', 'cobol'), isNull);
    expect(highlightSync('a' * (highlightLimit + 1), 'dart'), isNull);
  });

  test('large input highlights off the main isolate', () async {
    final code = List.filled(2000, 'func f() int { return 1 } // x').join('\n');
    final hl = await highlightLines(code, 'go');
    expect(hl, isNotNull);
    expect(hl!.length, 2000);
    expect(tokOf(hl, 1999, 'return'), Tok.keyword);
    expect(tokOf(hl, 1999, '// x'), Tok.comment);
  });

  test('scopes map to desktop token colors', () {
    expect(tokForScope('title.function'), Tok.function);
    expect(tokForScope('title.class.inherited'), Tok.type);
    expect(tokForScope('subst'), Tok.plain);
    expect(tokForScope('language:css'), isNull);
    expect(tokForScope('params'), isNull);
  });
}
