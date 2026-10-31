import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/file_icons.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  // 和桌面 fileicons_test.go 的 TestKindOf 同一批：表是生成的，这里核对手写的前后缀规则和查表顺序
  test('file kinds follow the desktop rules', () {
    final cases = {
      'main.go': FileKind.go,
      'internal/app/view.go': FileKind.go,
      'App.TSX': FileKind.ts,
      'package.json': FileKind.data,
      'package-lock.json': FileKind.lock,
      'Cargo.lock': FileKind.lock,
      'Makefile': FileKind.shell,
      'Dockerfile.dev': FileKind.docker,
      'app.Dockerfile': FileKind.docker,
      'docker-compose.override.yml': FileKind.docker,
      'compose.dev.yaml': FileKind.docker,
      '.env': FileKind.data,
      '.env.local': FileKind.data,
      'lib/main.dart': FileKind.dart,
      'pubspec.yaml': FileKind.flutter,
      'build.gradle.kts': FileKind.gradle,
      'AndroidManifest.xml': FileKind.android,
      '.gitignore': FileKind.git,
      'LICENSE': FileKind.text,
      'logo.PNG': FileKind.image,
      'noext': FileKind.other,
      'archive.tar.gz': FileKind.archive,
      'docs/design/flow.mmd': FileKind.other,
    };
    cases.forEach((name, want) => expect(fileKindOf(name), want, reason: name));
  });

  test('Xcode package dirs have their own kind, others are folders', () {
    expect(dirKindOf('Runner.xcodeproj'), FileKind.xcodeProj);
    expect(dirKindOf('Assets.xcassets'), FileKind.assets);
    expect(dirKindOf('LaunchImage.imageset'), FileKind.image);
    expect(dirKindOf('Base.lproj'), FileKind.locale);
    expect(dirKindOf('lib'), isNull);
    expect(dirKindOf('.bundle'), isNull);
  });

  testWidgets('every kind draws', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Wrap(
          children: [
            for (final k in FileKind.values)
              SvgPicture.string(k.svg, width: Ic.medium, height: Ic.medium),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
  });
}
