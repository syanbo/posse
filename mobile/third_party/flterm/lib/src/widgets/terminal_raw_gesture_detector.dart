import 'package:flutter/gestures.dart';
import 'package:flutter/widgets.dart';
import 'package:meta/meta.dart';

/// Gesture detector that recognizes taps and mouse drags.
///
/// Drag is restricted to mouse devices. Touch long-press selection is timed
/// by [TerminalGestureDetector] and does not compete with scrolling.
///
/// ```dart
/// TerminalRawGestureDetector(
///   onTapDown: (details) => handleTapDown(details),
///   onTapUp: (details) => handleTapUp(details),
///   onDragStart: (details) => handleDragStart(details),
///   child: Container(),
/// )
/// ```
@internal
class TerminalRawGestureDetector extends StatelessWidget {
  final Widget child;

  /// Fires when a tap begins.
  final GestureTapDownCallback? onTapDown;

  /// Fires when a tap ends.
  final GestureTapUpCallback? onTapUp;

  /// Fires when a tap loses the arena, for example to a scroll.
  final GestureTapCancelCallback? onTapCancel;

  /// Fires when a mouse drag begins.
  final GestureDragStartCallback? onDragStart;

  /// Fires as the mouse drag continues.
  final GestureDragUpdateCallback? onDragUpdate;

  /// Fires when a mouse drag ends or is cancelled.
  final VoidCallback? onDragEnd;

  const TerminalRawGestureDetector({
    super.key,
    required this.child,
    this.onTapDown,
    this.onTapUp,
    this.onTapCancel,
    this.onDragStart,
    this.onDragUpdate,
    this.onDragEnd,
  });

  @override
  Widget build(BuildContext context) {
    return RawGestureDetector(
      behavior: HitTestBehavior.opaque,
      gestures: <Type, GestureRecognizerFactory>{
        TapGestureRecognizer:
            GestureRecognizerFactoryWithHandlers<TapGestureRecognizer>(
              () => TapGestureRecognizer(debugOwner: this),
              (instance) => instance
                ..onTapDown = onTapDown
                ..onTapUp = onTapUp
                ..onTapCancel = onTapCancel,
            ),
        PanGestureRecognizer:
            GestureRecognizerFactoryWithHandlers<PanGestureRecognizer>(
              () => PanGestureRecognizer(
                debugOwner: this,
                supportedDevices: const {PointerDeviceKind.mouse},
              ),
              (instance) {
                instance
                  ..dragStartBehavior = .down
                  ..onStart = onDragStart
                  ..onUpdate = onDragUpdate
                  ..onEnd = (_) => onDragEnd?.call();
                instance.onCancel = () => onDragEnd?.call();
              },
            ),
      },
      child: child,
    );
  }
}
