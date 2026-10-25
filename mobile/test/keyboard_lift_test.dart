import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/terminal/keyboard_lift.dart';

void main() {
  test('键盘收着时不挪', () {
    expect(keyboardLift(contentBottom: 600, visible: 600, covered: 0), 0);
  });

  test('内容在键盘上面露得出来时不挪', () {
    // shell 刚开，提示符在第二行
    expect(keyboardLift(contentBottom: 40, visible: 300, covered: 300), 0);
  });

  test('只挪到内容底边露出来，再留一点空', () {
    expect(
      keyboardLift(contentBottom: 400, visible: 300, covered: 300, gap: 6),
      106,
    );
  });

  test('最多挪键盘盖住的高度', () {
    // 全屏程序（grok、备用屏幕）铺满整屏，终端底边贴着快捷键栏
    expect(keyboardLift(contentBottom: 600, visible: 300, covered: 300), 300);
  });
}
