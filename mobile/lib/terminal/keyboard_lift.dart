/// 弹键盘时终端往上挪多少。纯计算，不碰终端控件。
///
/// 终端不改高度，键盘盖住底下 [covered] 那么高，只剩上面 [visible] 看得见。
/// 内容底边（[contentBottom]，从终端顶端量）露得出来就不挪；露不出来就挪到它贴着
/// 快捷键栏、再留 [gap] 的空，最多挪 [covered]，这时终端底边正好贴着快捷键栏。
double keyboardLift({
  required double contentBottom,
  required double visible,
  required double covered,
  double gap = 6,
}) {
  if (covered <= 0) return 0;
  final need = contentBottom + gap - visible;
  if (need <= 0) return 0;
  return need > covered ? covered : need;
}
