import 'package:flutter/material.dart';

/// 复制按钮贴在选区上沿的上方，水平落在这一行选中的中间。
/// 上面放不下就改到这一行下面，再夹在看得见的终端里。
class CopyChipPlace {
  final double top;
  final double centerX;

  const CopyChipPlace({required this.top, required this.centerX});
}

CopyChipPlace copyChipPlace({
  required int topRow,
  required int leftCol,
  required int rightCol,
  required double cellWidth,
  required double cellHeight,
  required double padLeft,
  required double padTop,
  required double lift,
  required double pinch,
  required double panX,
  required double panY,
  required double cropX,
  required double viewWidth,
  required double viewHeight,
  double chipWidth = 56,
  double chipHeight = 32,
  double gap = 6,
  double margin = 4,
}) {
  final scale = pinch == 0 ? 1.0 : pinch;
  final rowTop = padTop - lift + panY + scale * topRow * cellHeight;
  final left = padLeft - cropX + panX + scale * leftCol * cellWidth;
  final right = padLeft - cropX + panX + scale * (rightCol + 1) * cellWidth;
  final above = rowTop - gap - chipHeight;
  final below = rowTop + scale * cellHeight + gap;
  var top = above >= margin ? above : below;
  final lowest = viewHeight - chipHeight - margin;
  if (top > lowest) top = lowest;
  if (top < margin) top = margin;

  var center = (left + right) / 2;
  final half = chipWidth / 2;
  final minCenter = half + margin;
  final maxCenter = viewWidth - half - margin;
  if (maxCenter <= minCenter) {
    center = viewWidth / 2;
  } else if (center < minCenter) {
    center = minCenter;
  } else if (center > maxCenter) {
    center = maxCenter;
  }
  return CopyChipPlace(top: top, centerX: center);
}

/// 选区边界在终端画面上的点。[after] 为真时是这一格的右缘。
Offset selectionCaret({
  required int row,
  required int col,
  required bool after,
  required double cellWidth,
  required double cellHeight,
  required double padLeft,
  required double padTop,
  required double lift,
  required double pinch,
  required double panX,
  required double panY,
  required double cropX,
}) {
  final scale = pinch == 0 ? 1.0 : pinch;
  final boundary = after ? col + 1 : col;
  return Offset(
    padLeft - cropX + panX + scale * boundary * cellWidth,
    padTop - lift + panY + scale * row * cellHeight,
  );
}

/// 画面上的点对应哪一格。[fingerLift] 把触点往上移，拖把手时字留在手指上面。
({int row, int col}) viewCellAt({
  required double x,
  required double y,
  required double fingerLift,
  required double cellWidth,
  required double cellHeight,
  required double padLeft,
  required double padTop,
  required double lift,
  required double pinch,
  required double panX,
  required double panY,
  required double cropX,
}) {
  final scale = pinch == 0 ? 1.0 : pinch;
  final gx = (x - padLeft + cropX - panX) / scale;
  final gy = (y - fingerLift - padTop + lift - panY) / scale;
  final row = cellHeight > 0 ? (gy / cellHeight).floor() : 0;
  final col = cellWidth > 0 ? (gx / cellWidth).floor() : 0;
  return (row: row, col: col);
}
