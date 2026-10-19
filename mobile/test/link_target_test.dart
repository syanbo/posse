import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/terminal/link_target.dart';

const root = '/Users/me/code/app';

String? fileOf(LinkTarget? target) => target is OpenFile ? target.path : null;

void main() {
  test('网址交给浏览器，别的协议不管', () {
    final web = resolveLink(uri: Uri.parse('https://example.com/a?b=1'));
    expect(web, isA<OpenUrl>());
    expect((web as OpenUrl).uri.host, 'example.com');
    expect(resolveLink(uri: Uri.parse('mailto:a@b.c')), isA<OpenUrl>());
    expect(resolveLink(uri: Uri.parse('vscode://file/x')), isNull);
  });

  test('工作区里的文件换成相对路径', () {
    expect(
      fileOf(resolveLink(filePath: 'lib/main.dart', root: root)),
      'lib/main.dart',
    );
    expect(
      fileOf(
        resolveLink(
          filePath: 'main.dart',
          resolvedPath: '$root/lib/./main.dart',
          root: '$root/',
        ),
      ),
      'lib/main.dart',
    );
    expect(
      fileOf(
        resolveLink(uri: Uri.parse('file://$root/docs/a%20b.md'), root: root),
      ),
      'docs/a b.md',
    );
  });

  test('/private 前缀两边都认', () {
    expect(
      fileOf(resolveLink(filePath: '/private/tmp/w/x.png', root: '/tmp/w')),
      'x.png',
    );
    expect(
      fileOf(resolveLink(filePath: '/var/w/x.png', root: '/private/var/w')),
      'x.png',
    );
  });

  test('工作区外、退出工作区、不知道工作区路径的都打不开', () {
    expect(
      resolveLink(filePath: '/tmp/shot.png', root: root),
      isA<NotInWorkspace>(),
    );
    expect(
      resolveLink(filePath: '/Users/me/code/application/a', root: root),
      isA<NotInWorkspace>(),
    );
    expect(resolveLink(filePath: '/a/b', root: null), isA<NotInWorkspace>());
    expect(
      resolveLink(filePath: '~/notes.md', root: root),
      isA<NotInWorkspace>(),
    );
    expect(resolveLink(filePath: '../secret', root: root), isNull);
    expect(resolveLink(filePath: root, root: root), isNull);
  });
}
