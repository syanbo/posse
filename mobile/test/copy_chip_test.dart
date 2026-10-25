import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/terminal/copy_chip.dart';

void main() {
  CopyChipPlace place({
    int topRow = 4,
    int leftCol = 2,
    int rightCol = 8,
    double lift = 0,
    double viewHeight = 400,
    double viewWidth = 300,
    double cropX = 0,
    double pinch = 1,
    double panX = 0,
    double panY = 0,
  }) {
    return copyChipPlace(
      topRow: topRow,
      leftCol: leftCol,
      rightCol: rightCol,
      cellWidth: 10,
      cellHeight: 20,
      padLeft: 4,
      padTop: 4,
      lift: lift,
      pinch: pinch,
      panX: panX,
      panY: panY,
      cropX: cropX,
      viewWidth: viewWidth,
      viewHeight: viewHeight,
      chipWidth: 56,
      chipHeight: 32,
    );
  }

  test('复制按钮在选区上沿的上方，对准这一行选中的中间', () {
    // 第 4 行顶在 4 + 4*20 = 84，按钮高 32、间隔 6，顶在 84-38 = 46
    // 列 2 到 8（含）的中线：左边 4+20，右边 4+90，中点 59
    final got = place();
    expect(got.top, 46);
    expect(got.centerX, 59);
  });

  test('贴着屏幕上沿时改到这一行下面', () {
    // 第 0 行顶在 4，上面放不下 32+6
    final got = place(topRow: 0, leftCol: 0, rightCol: 1);
    expect(got.top, 4 + 20 + 6);
  });

  test('键盘把终端往上挪时，按钮跟着选区', () {
    final still = place();
    final lifted = place(lift: 30);
    expect(lifted.top, still.top - 30);
  });

  test('靠近右边缘时不伸出屏幕', () {
    final got = place(leftCol: 28, rightCol: 30, viewWidth: 200);
    expect(got.centerX, lessThanOrEqualTo(200 - 28 - 4));
  });

  test('格子左缘和触点能互相换算', () {
    const args = (
      cellWidth: 10.0,
      cellHeight: 20.0,
      padLeft: 4.0,
      padTop: 4.0,
      lift: 0.0,
      pinch: 1.0,
      panX: 0.0,
      panY: 0.0,
      cropX: 0.0,
    );
    final caret = selectionCaret(
      row: 3,
      col: 5,
      after: false,
      cellWidth: args.cellWidth,
      cellHeight: args.cellHeight,
      padLeft: args.padLeft,
      padTop: args.padTop,
      lift: args.lift,
      pinch: args.pinch,
      panX: args.panX,
      panY: args.panY,
      cropX: args.cropX,
    );
    final cell = viewCellAt(
      x: caret.dx + 1,
      y: caret.dy + 1,
      fingerLift: 0,
      cellWidth: args.cellWidth,
      cellHeight: args.cellHeight,
      padLeft: args.padLeft,
      padTop: args.padTop,
      lift: args.lift,
      pinch: args.pinch,
      panX: args.panX,
      panY: args.panY,
      cropX: args.cropX,
    );
    expect(cell.row, 3);
    expect(cell.col, 5);
  });
}
