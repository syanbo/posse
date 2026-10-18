import 'dart:ui' show Canvas;

import 'package:libghostty/libghostty.dart';

import 'atlas/atlas.dart';
import 'atlas/sprite_buffer.dart';
import 'kitty_image_cache.dart';
import 'kitty_placement_cache.dart';
import 'paint_state.dart';
import 'painters/background_painter.dart';
import 'painters/cursor_painter.dart';
import 'painters/decoration_painter.dart';
import 'painters/emoji_painter.dart';
import 'painters/kitty_graphics_painter.dart';
import 'painters/shaped_run_painter.dart';
import 'painters/sprite_painter.dart';
import 'painters/terminal_text_painter.dart';
import 'painters/underline_painter.dart';

/// Owns paint helpers, paint order, and paint-only terminal resources.
final class TerminalPainterStack {
  // The protocol splits negative z values in half at INT32_MIN / 2.
  static const int _kittyBelowBackgroundThreshold = -1 << 30;

  final SpriteBuffer _sprites;
  final TerminalPaintState _state;
  final KittyImageCache _kittyImageCache;
  final List<KittyPlacementSnapshot> _kittyBelowBackground = [];
  final List<KittyPlacementSnapshot> _kittyBelowText = [];
  final List<KittyPlacementSnapshot> _kittyAboveText = [];
  final ShapedRunPainter _shapedRunPainter;
  final BackgroundPainter _backgroundPainter;
  final DecorationPainter _decorationPainter;
  late final KittyGraphicsPainter _kittyBelowBackgroundPainter;
  late final KittyGraphicsPainter _kittyBelowTextPainter;
  late final KittyGraphicsPainter _kittyAboveTextPainter;
  late final KittyPlacementCache _kittyPlacementCache;

  late EmojiPainter _emojiPainter;
  late SpritePainter _spritePainter;
  late CursorPainter _cursorPainter;
  late TerminalTextPainter _textPainter;
  late UnderlinePainter _underlinePainter;

  TerminalPainterStack({
    required Atlas atlas,
    required this._sprites,
    required this._state,
    required void Function() onImageReady,
  }) : _kittyImageCache = KittyImageCache(onImageReady: onImageReady),
       _shapedRunPainter = ShapedRunPainter(_sprites.shaped),
       _backgroundPainter = BackgroundPainter(_state, _sprites),
       _decorationPainter = DecorationPainter(_sprites) {
    _kittyPlacementCache = KittyPlacementCache(
      state: _state,
      images: _kittyImageCache,
    );
    _kittyBelowBackgroundPainter = KittyGraphicsPainter(
      state: _state,
      cache: _kittyImageCache,
      snapshots: _kittyBelowBackground,
    );
    _kittyBelowTextPainter = KittyGraphicsPainter(
      state: _state,
      cache: _kittyImageCache,
      snapshots: _kittyBelowText,
    );
    _kittyAboveTextPainter = KittyGraphicsPainter(
      state: _state,
      cache: _kittyImageCache,
      snapshots: _kittyAboveText,
    );
    bindAtlas(atlas);
  }

  void bindAtlas(Atlas atlas) {
    _textPainter = TerminalTextPainter(atlas, _sprites.wide, _sprites.regular);
    _spritePainter = SpritePainter(atlas, _sprites);
    _cursorPainter = CursorPainter(_state, atlas);
    _emojiPainter = EmojiPainter(atlas, _sprites);
    _underlinePainter = UnderlinePainter(atlas, _sprites);
  }

  void dispose() => _kittyImageCache.dispose();

  void paint(Canvas canvas) {
    _kittyBelowBackgroundPainter.paint(canvas);
    _backgroundPainter.paint(canvas);
    _kittyBelowTextPainter.paint(canvas);
    _underlinePainter.paint(canvas);
    _textPainter.paint(canvas);
    _shapedRunPainter.paint(canvas);
    _spritePainter.paint(canvas);
    _cursorPainter.paint(canvas);
    _emojiPainter.paint(canvas);
    _decorationPainter.paint(canvas);
    _kittyAboveTextPainter.paint(canvas);
  }

  void sync(Terminal terminal, {required bool geometryDirty}) {
    if (!_kittyPlacementCache.sync(terminal, geometryDirty: geometryDirty)) {
      return;
    }
    _rebuildKittyLayers();
  }

  void _rebuildKittyLayers() {
    _kittyBelowBackground.clear();
    _kittyBelowText.clear();
    _kittyAboveText.clear();
    for (final snapshot in _kittyPlacementCache.snapshots) {
      if (snapshot.z >= 0) {
        _kittyAboveText.add(snapshot);
      } else if (snapshot.z < _kittyBelowBackgroundThreshold) {
        _kittyBelowBackground.add(snapshot);
      } else {
        _kittyBelowText.add(snapshot);
      }
    }
  }
}
