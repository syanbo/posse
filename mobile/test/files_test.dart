import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/files/files.dart';

void main() {
  test('directories come first and paths join the parent', () {
    final rows = parseDir('lib', [
      {'name': 'main.dart', 'isDirectory': false},
      {'name': 'ui', 'isDirectory': true},
      {'name': 'App.dart', 'isDirectory': false},
      {'name': ''},
      'junk',
    ]);
    expect(rows.map((e) => e.path), [
      'lib/ui',
      'lib/App.dart',
      'lib/main.dart',
    ]);
    expect(rows.first.dir, isTrue);
    expect(
      parseDir('', [
        {'name': 'a', 'isDirectory': false},
      ]).single.path,
      'a',
    );
    expect(parseDir('', null), isEmpty);
  });
}
