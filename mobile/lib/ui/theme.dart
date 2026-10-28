import 'package:flutter/material.dart';

// 设计约定和桌面端 internal/app/style.go 一致：暖灰底色分顶栏、画布、卡片几层，靠明度差和 8% 透明细线分层；
// 单闭合边框（卡片里的按钮和输入不再加框，用填充底）；陶土色给主按钮、焦点、链接、图标和选中项；
// 只有"待处理"用琥珀色加文字；状态用蜂巢格形状区分（实心工作中、空心带点待处理、空心完成）；
// 一行里的几项信息靠间距和图标分开，不用"A · B"中点串。字重三档：400 正文、500 列表标题和按钮、600 页面和卡片标题。

/// 字号阶梯。手机比桌面大一档，阅读距离更远。
abstract final class Fs {
  static const caption = 12.0;
  static const meta = 13.0;
  static const body = 15.0;
  static const read = 15.5;
  static const cardTitle = 16.0;
  static const pageTitle = 28.0;
}

/// 圆角：外圆角 = 内圆角 + 内边距。
abstract final class Rd {
  static const button = 8.0;
  static const input = 10.0;
  static const code = 10.0;
  static const card = 14.0;
  static const bubble = 16.0;
  static const composer = 20.0;
}

/// 等宽字体。iOS 用 Menlo，中文落到苹方；安卓没有 Menlo，落到 monospace（Droid Sans Mono）。
/// 不能写 Courier：安卓把它映射成笔画很细的打字机字体 Cutive Mono，代码和终端看着灰蒙蒙的。
const monoFamily = 'Menlo';
// Symbols Nerd Font Mono 是 App 自带的，补上终端提示符里 Nerd Font 的图标（见 pubspec.yaml）
const monoFallback = ['monospace', 'Symbols Nerd Font Mono', 'PingFang SC'];

/// 会话页、终端页标题下面那一行（状态、上下文、延迟）的文字。写明字重：不写会继承顶栏标题的粗体。
TextStyle statusLineStyle(Color color) => TextStyle(
  fontSize: 12,
  fontWeight: FontWeight.w400,
  color: color,
  fontFeatures: const [FontFeature.tabularFigures()],
);

TextStyle mono(double size, {Color? color, double? height}) => TextStyle(
  fontFamily: monoFamily,
  fontFamilyFallback: monoFallback,
  fontSize: size,
  color: color,
  height: height,
);

@immutable
class PosseColors extends ThemeExtension<PosseColors> {
  const PosseColors({
    required this.dark,
    required this.accent,
    required this.text,
    required this.muted,
    required this.faint,
    required this.honey,
    required this.sage,
    required this.danger,
    required this.band,
    required this.canvas,
    required this.card,
    required this.raised,
    required this.line,
    required this.tint,
    required this.wash,
    required this.fill,
    required this.code,
  });

  final bool dark;
  final Color accent;
  final Color text;
  final Color muted;
  final Color faint; // 最弱的文字：占位、不重要的计数
  final Color honey; // 等你处理
  final Color sage; // 本轮完成、新增
  final Color danger;
  final Color band; // 顶栏
  final Color canvas; // 页面的底，卡片放在上面
  final Color card;
  final Color raised; // 浮层：比卡片再高一层
  final Color line; // 分隔线
  final Color tint; // 选中项、自己的消息：强调色淡底
  final Color wash; // 按下的底
  final Color fill; // 卡片里次级按钮、输入的底
  final Color code; // 代码块的底

  static const light = PosseColors(
    dark: false,
    accent: Color(0xFFC15F3C),
    text: Color(0xFF1F1E1D),
    muted: Color(0xFF6B6966),
    faint: Color(0xFF999793),
    honey: Color(0xFFB5700E),
    sage: Color(0xFF3E8A4C),
    danger: Color(0xFFC4352B),
    band: Color(0xFFF8F7F5),
    canvas: Color(0xFFF3F2EF),
    card: Color(0xFFFFFFFF),
    raised: Color(0xFFFFFFFF),
    line: Color(0x14000000),
    tint: Color(0x1FC15F3C),
    wash: Color(0x0A000000),
    fill: Color(0x0F000000),
    code: Color(0xFFF5F4F1),
  );

  static const darkColors = PosseColors(
    dark: true,
    accent: Color(0xFFD97757),
    text: Color(0xFFECEBE8),
    muted: Color(0xFFB4B2AC),
    faint: Color(0xFF7D7B76),
    honey: Color(0xFFF2AA2E),
    sage: Color(0xFF8DBB92),
    danger: Color(0xFFE5675C),
    band: Color(0xFF1C1C1B),
    canvas: Color(0xFF1A1A19),
    card: Color(0xFF262624),
    raised: Color(0xFF2E2E2B),
    line: Color(0x14FFFFFF),
    tint: Color(0x33D97757),
    wash: Color(0x0DFFFFFF),
    fill: Color(0x17FFFFFF),
    code: Color(0xFF141413),
  );

  /// 没装 Posse 主题（比如测试里的裸 MaterialApp）时按明暗取默认色板。
  static PosseColors of(BuildContext context) {
    final theme = Theme.of(context);
    return theme.extension<PosseColors>() ??
        (theme.brightness == Brightness.dark ? darkColors : light);
  }

  @override
  PosseColors copyWith() => this;

  @override
  PosseColors lerp(PosseColors? other, double t) =>
      other == null || t < 0.5 ? this : other;
}

ThemeData posseTheme(Brightness brightness) {
  final c = brightness == Brightness.dark
      ? PosseColors.darkColors
      : PosseColors.light;
  final scheme =
      ColorScheme.fromSeed(
        seedColor: c.accent,
        brightness: brightness,
      ).copyWith(
        primary: c.accent,
        onPrimary: Colors.white,
        secondary: c.accent,
        surface: c.card,
        onSurface: c.text,
        onSurfaceVariant: c.muted,
        error: c.danger,
        outline: c.line,
        outlineVariant: c.line,
        surfaceTint: Colors.transparent,
      );
  final buttonShape = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(Rd.button + 2),
  );
  return ThemeData(
    useMaterial3: true,
    brightness: brightness,
    colorScheme: scheme,
    scaffoldBackgroundColor: c.canvas,
    canvasColor: c.canvas,
    dividerColor: c.line,
    splashFactory: NoSplash.splashFactory,
    highlightColor: c.wash,
    extensions: [c],
    textSelectionTheme: TextSelectionThemeData(
      cursorColor: c.accent,
      selectionColor: c.accent.withValues(alpha: 0.3),
      selectionHandleColor: c.accent,
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: c.canvas,
      foregroundColor: c.text,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      titleTextStyle: TextStyle(
        fontSize: 17,
        fontWeight: FontWeight.w600,
        color: c.text,
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: c.accent,
        foregroundColor: Colors.white,
        disabledBackgroundColor: c.fill,
        disabledForegroundColor: c.faint,
        minimumSize: const Size(0, 44),
        shape: buttonShape,
        textStyle: const TextStyle(fontSize: 15, fontWeight: FontWeight.w500),
        elevation: 0,
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        foregroundColor: c.text,
        backgroundColor: c.fill,
        minimumSize: const Size(0, 44),
        shape: buttonShape,
        textStyle: const TextStyle(fontSize: 15, fontWeight: FontWeight.w500),
      ),
    ),
    iconButtonTheme: IconButtonThemeData(
      style: IconButton.styleFrom(foregroundColor: c.text),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: c.fill,
      hintStyle: TextStyle(color: c.faint),
      contentPadding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide.none,
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide.none,
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide(color: c.accent.withValues(alpha: 0.6)),
      ),
    ),
    bottomSheetTheme: BottomSheetThemeData(
      backgroundColor: c.raised,
      surfaceTintColor: Colors.transparent,
      dragHandleColor: c.faint,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(Rd.card + 4)),
      ),
    ),
    popupMenuTheme: PopupMenuThemeData(
      color: c.raised,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.card),
      ),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: c.raised,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.card + 4),
      ),
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(
      color: c.accent,
      linearTrackColor: c.fill,
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: c.dark ? c.raised : const Color(0xFF2A2A27),
      contentTextStyle: const TextStyle(color: Colors.white, fontSize: 14),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.input),
      ),
    ),
  );
}

/// 系统要求少用动画。安卓的「移除动画」在 [MediaQueryData.disableAnimations]；iOS 的「减弱动态效果」
/// 不设那一项，只在 accessibilityFeatures.reduceMotion，两个都要看。
bool reduceMotionOf(BuildContext context) =>
    MediaQuery.disableAnimationsOf(context) ||
    View.of(context).platformDispatcher.accessibilityFeatures.reduceMotion;
