import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/terminal/desk_fit.dart';

void main() {
  test('适配比例缩到视口宽度，接近铺满或更宽都当 1', () {
    expect(
      deskFitScale(viewportWidth: 100, gridWidth: 400),
      closeTo(0.25, 1e-9),
    );
    expect(deskFitScale(viewportWidth: 100, gridWidth: 100), 1);
    expect(deskFitScale(viewportWidth: 100, gridWidth: 104), 1);
    expect(deskFitScale(viewportWidth: 200, gridWidth: 100), 1);
    expect(deskFitScale(viewportWidth: 0, gridWidth: 100), 1);
  });

  test('倍数吸到固定档', () {
    expect(snapDeskZoom(0.6), 0.5);
    expect(snapDeskZoom(0.7), 0.75);
    expect(snapDeskZoom(1.1), 1);
    expect(snapDeskZoom(1.2), 1.25);
    expect(snapDeskZoom(3), 2);
  });

  test('平移夹在边缘，没超出的轴钉在左上', () {
    final wide = clampDeskPan(
      panX: 10,
      panY: -40,
      contentWidth: 200,
      contentHeight: 50,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(wide.x, 0);
    expect(wide.y, 0);

    final mid = clampDeskPan(
      panX: -40,
      panY: -20,
      contentWidth: 200,
      contentHeight: 180,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(mid.x, -40);
    expect(mid.y, -20);

    final far = clampDeskPan(
      panX: -150,
      panY: -200,
      contentWidth: 200,
      contentHeight: 180,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(far.x, -100);
    expect(far.y, -80);
  });

  test('一根手指：超出才平移，到顶后剩下的滚回滚区', () {
    final inside = deskDrag(
      panX: -20,
      panY: -10,
      dx: -5,
      dy: -4,
      contentWidth: 200,
      contentHeight: 180,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(inside.panX, -25);
    expect(inside.panY, -14);
    expect(inside.scrollDy, 0);

    final intoHistory = deskDrag(
      panX: 0,
      panY: 0,
      dx: 0,
      dy: 12,
      contentWidth: 200,
      contentHeight: 180,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(intoHistory.panY, 0);
    expect(intoHistory.scrollDy, 12);

    final fits = deskDrag(
      panX: 0,
      panY: 0,
      dx: 8,
      dy: -6,
      contentWidth: 80,
      contentHeight: 40,
      viewportWidth: 100,
      viewportHeight: 100,
    );
    expect(fits.panX, 0);
    expect(fits.panY, 0);
    expect(fits.scrollDy, -6);
  });
}
