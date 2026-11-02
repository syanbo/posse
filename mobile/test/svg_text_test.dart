import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/files/svg_text.dart';

// img.shields.io/badge/build-passing-brightgreen 的原文
const _badge =
    r'''<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20" role="img" aria-label="build: passing"><title>build: passing</title><filter id="blur"><feGaussianBlur stdDeviation="16"/></filter><linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient><clipPath id="r"><rect width="88" height="20" rx="3"/></clipPath><g clip-path="url(#r)"><rect width="37" height="20" fill="#555"/><rect x="37" width="51" height="20" fill="#4b0"/><rect width="88" height="20" fill="url(#s)"/></g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="110"><g transform="scale(.1)"><g aria-hidden="true" fill="#010101"><text x="195" y="150" fill-opacity=".8" filter="url(#blur)" textLength="270">build</text><text x="195" y="150" fill-opacity=".3" textLength="270">build</text></g><text x="195" y="140" textLength="270">build</text></g><g transform="scale(.1)"><g aria-hidden="true" fill="#010101"><text x="615" y="150" fill-opacity=".8" filter="url(#blur)" textLength="410">passing</text><text x="615" y="150" fill-opacity=".3" textLength="410">passing</text></g><text x="615" y="140" textLength="410">passing</text></g></g></svg>''';

void main() {
  test('shields badge text is scaled down and blurred shadows dropped', () {
    final out = utf8.decode(fixSvgText(utf8.encode(_badge)));
    expect(out, isNot(contains('scale(')));
    expect(out, isNot(contains('filter="url(#blur)"')));
    expect(
      out,
      contains(
        '<text x="19.5" y="14" textLength="27" font-size="11">build</text>',
      ),
    );
    expect(out, contains('fill-opacity=".3" textLength="27" font-size="11"'));
  });

  test('tspans inside a scaled group are scaled too', () {
    final out = utf8.decode(
      fixSvgText(
        utf8.encode(
          '<svg xmlns="http://www.w3.org/2000/svg"><g font-size="110"><g transform="scale(.1)"><text x="100" y="100">a<tspan x="200" font-size="50">b</tspan><tspan dx="30">c</tspan></text></g></g></svg>',
        ),
      ),
    );
    expect(out, contains('<tspan x="20" font-size="5">b</tspan>'));
    expect(out, contains('<tspan dx="3" font-size="11">c</tspan>'));
  });

  test('svg without text tricks is left alone', () {
    final plain = utf8.encode(
      '<svg xmlns="http://www.w3.org/2000/svg"><g transform="scale(2)"><rect width="1" height="1"/><text>x</text></g></svg>',
    );
    expect(identical(fixSvgText(plain), plain), isTrue);
  });
}
