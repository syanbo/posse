// ignore: unused_import
import 'package:intl/intl.dart' as intl;

import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for English (`en`).
class AppLocalizationsEn extends AppLocalizations {
  AppLocalizationsEn([String locale = 'en']) : super(locale);

  @override
  String get commonRefresh => 'Refresh';

  @override
  String get commonResetToDefault => 'Reset to Default';

  @override
  String get commonRetry => 'Retry';

  @override
  String get deskAddressRequired => 'Enter an address first';

  @override
  String get deskAlreadyUnlocked =>
      'The Mac is already unlocked. The password wasn’t sent.';

  @override
  String get deskBack => 'Back';

  @override
  String get deskCaptureFailed => 'Capture failed';

  @override
  String get deskChangePasswordTip => 'Change or Forget Password';

  @override
  String get deskConnecting => 'Connecting…';

  @override
  String get deskConnectionNote =>
      'By default the picture goes to the connected computer by subscription, and the computer picks the port. This changes the UDP address, for debugging.';

  @override
  String get deskConnectionSettings => 'Connection Settings';

  @override
  String get deskDebug => 'Debug';

  @override
  String deskDebugDecode(String ms) {
    return 'Decode $ms ms';
  }

  @override
  String deskDebugHeadline(String fps, String received) {
    return '$fps  received $received fps';
  }

  @override
  String deskDebugInput(String sent, String acked) {
    return 'Input: sent $sent, acked $acked';
  }

  @override
  String deskDebugKeyframes(String count) {
    return 'Keyframes $count';
  }

  @override
  String deskDebugLoss(String loss) {
    return 'Loss $loss';
  }

  @override
  String deskDebugPath(String path) {
    return 'Path $path';
  }

  @override
  String deskDebugRate(String bitrate, String bytes, String peak) {
    return 'Bitrate $bitrate ($bytes)  peak $peak';
  }

  @override
  String deskDebugView(String zoom, String x, String y) {
    return 'Zoom $zoom  pan $x, $y';
  }

  @override
  String get deskDisconnect => 'Disconnect';

  @override
  String get deskDisconnected => 'Disconnected';

  @override
  String deskDisplayNumbered(String number) {
    return 'Display $number';
  }

  @override
  String get deskEndGeneric => 'Connection ended';

  @override
  String get deskEndNoReport =>
      'The computer received no reports from the phone';

  @override
  String get deskEndStopped => 'Stopped on the Mac';

  @override
  String get deskEndUnsubscribed => 'Stopped viewing';

  @override
  String get deskEnterPassword => 'Enter Password';

  @override
  String get deskEntireScreen => 'Entire Screen';

  @override
  String deskEventClick(String point) {
    return 'Click $point';
  }

  @override
  String deskEventClicks(int count, String point) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count clicks',
      one: '$count click',
    );
    return '$_temp0 $point';
  }

  @override
  String deskEventDown(String point) {
    return 'Down $point';
  }

  @override
  String deskEventMove(String point) {
    return 'Drag $point';
  }

  @override
  String deskEventPoint(String point) {
    return 'Point $point';
  }

  @override
  String deskEventRightClick(String point) {
    return 'Right-click $point';
  }

  @override
  String deskEventScroll(String dx, String dy) {
    return 'Scroll $dx, $dy';
  }

  @override
  String deskEventScrollInertia(String dx, String dy) {
    return 'Scroll $dx, $dy inertia';
  }

  @override
  String deskEventText(String text) {
    return 'Text $text';
  }

  @override
  String deskEventUp(String point) {
    return 'Up $point';
  }

  @override
  String get deskFailBusy => 'Another phone is viewing';

  @override
  String get deskFailCodec =>
      'This computer can’t encode video this phone can play';

  @override
  String get deskFailDisconnected => 'Connection lost';

  @override
  String get deskFailGeneric => 'Couldn’t connect';

  @override
  String get deskFailLan =>
      'This phone and the Mac aren’t on the same local network';

  @override
  String get deskFailNoPlugin => 'This device can’t view the desktop';

  @override
  String get deskFailOutdated => 'Posse on this computer needs an update';

  @override
  String get deskFailRoute => 'Neither a direct connection nor the relay works';

  @override
  String get deskFailScreenPermission => 'Allow screen recording on the Mac';

  @override
  String get deskForgetPassword => 'Forget Password';

  @override
  String get deskFpsStill => 'Still';

  @override
  String get deskGestureClick => 'Click';

  @override
  String get deskGestureDismiss =>
      'Tap anywhere to close. Change the mode under Remote Desktop in Settings.';

  @override
  String get deskGestureDrag => 'Drag';

  @override
  String get deskGestureLongPressDrag =>
      'Long-press until it buzzes, then drag';

  @override
  String get deskGestureMovePointer => 'Move Pointer';

  @override
  String get deskGesturePan => 'Pan';

  @override
  String get deskGesturePinch => 'Pinch';

  @override
  String get deskGestureRightClick => 'Right-Click';

  @override
  String get deskGestureScroll => 'Scroll';

  @override
  String get deskGestureSlideOne => 'Slide one finger';

  @override
  String get deskGestureTap => 'Tap one finger';

  @override
  String get deskGestureTapAtPointer => 'Tap one finger; clicks at the pointer';

  @override
  String get deskGestureThreeFingerDrag =>
      'Slide three fingers, or tap then hold and slide';

  @override
  String get deskGestureTitle => 'Gestures';

  @override
  String get deskGestureTwoFingerDrag => 'Drag two fingers';

  @override
  String get deskGestureTwoFingerTap => 'Tap two fingers';

  @override
  String get deskGestureZoom => 'Zoom';

  @override
  String get deskGestureZoomedDrag => 'Drag one finger when zoomed in';

  @override
  String get deskHost => 'Host';

  @override
  String get deskIndirect =>
      'The picture can’t get through the tunnel. On the same Wi-Fi, scan this Mac’s LAN code, or use Tailscale.';

  @override
  String get deskKeyboard => 'Keyboard';

  @override
  String get deskListingWindows => 'Listing windows…';

  @override
  String get deskMacLocked => 'Mac is locked';

  @override
  String get deskMainDisplay => 'Main Display';

  @override
  String get deskMissionControl => 'Mission Control';

  @override
  String get deskModeDirect => 'Direct Touch Mode';

  @override
  String get deskModeTrackpad => 'Trackpad Mode';

  @override
  String get deskNoAccessibility =>
      'View only. Turn on Accessibility for Posse on the Mac to control it.';

  @override
  String get deskNoComputer => 'No computer connected';

  @override
  String get deskPasswordForgotten => 'Saved password forgotten';

  @override
  String get deskPasswordHint => 'Login Password';

  @override
  String get deskPasswordSaveFailed => 'Couldn’t save the password';

  @override
  String get deskPasswordSaved =>
      'Password saved. Next time the Mac locks, tap “Unlock”.';

  @override
  String get deskPathLan => 'Direct';

  @override
  String get deskPathPunched => 'Direct (Hole-Punched)';

  @override
  String get deskPathRelay => 'Relay';

  @override
  String get deskPaused => 'Paused (in background)';

  @override
  String get deskPort => 'Port';

  @override
  String get deskPortRange => 'Port must be from 1 to 65535';

  @override
  String get deskReconnect => 'Reconnect';

  @override
  String get deskRemember => 'Remember';

  @override
  String deskResolutionMain(String width, String height) {
    return '$width × $height · Main';
  }

  @override
  String get deskScreenPermissionHint =>
      'On the Mac, turn on Posse in System Settings › Privacy & Security › Screen & System Audio Recording, then retry.';

  @override
  String get deskShortcuts => 'Shortcuts';

  @override
  String get deskSpaceLeft => '◀ Space';

  @override
  String get deskSpaceRight => 'Space ▶';

  @override
  String get deskToLandscape => 'Switch to Landscape';

  @override
  String get deskToPortrait => 'Switch to Portrait';

  @override
  String get deskUdpAddress => 'UDP Address';

  @override
  String get deskUnlock => 'Unlock';

  @override
  String get deskUnlockNote =>
      'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed.';

  @override
  String get deskUnlockNoteRemember =>
      'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed. “Remember” stores it on this phone only.';

  @override
  String get deskUse => 'Use';

  @override
  String get deskViewTip => 'Choose Screen or Window';

  @override
  String get deskWaitingPicture => 'Connected. Waiting for the picture…';

  @override
  String get deskWindowGone =>
      'The window closed or left this space. Showing the entire screen.';

  @override
  String get deskWindows => 'Windows';

  @override
  String get deskWindowsEmpty =>
      'No windows in this space can be viewed on their own';

  @override
  String get deskWindowsError => 'Couldn’t list windows. Try again later.';

  @override
  String get settingsLanguageSystem => 'System';

  @override
  String get settingsLanguageTitle => 'Language';

  @override
  String get usageCursorModels => 'Cursor Models';

  @override
  String usageIncludedSpend(String used, String limit, String percent) {
    return 'Included usage $used / $limit ($percent)';
  }

  @override
  String get usageListSeparator => ', ';

  @override
  String usageNamedWindow(String name, String window) {
    return '$name $window';
  }

  @override
  String get usageNotConnected => 'The computer isn’t connected';

  @override
  String get usageOldPosse =>
      'Posse on the computer is too old. Update it to refresh.';

  @override
  String usageOnDemand(String used, String stale) {
    return 'On-demand usage $used$stale';
  }

  @override
  String usageOnDemandLimited(String used, String limit, String stale) {
    return 'On-demand usage $used, limit $limit$stale';
  }

  @override
  String get usageOtherModels => 'Other Models';

  @override
  String get usagePerMonth => '/mo';

  @override
  String usageReadingAge(String reset, String time) {
    return '$reset, reading from $time';
  }

  @override
  String get usageRemaining => 'Remaining';

  @override
  String usageResetCards(int count, String stale) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count reset cards',
      one: '$count reset card',
    );
    return '$_temp0$stale';
  }

  @override
  String usageResetCardsExpiring(int count, String date, String stale) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count reset cards',
      one: '$count reset card',
    );
    return '$_temp0, earliest expires $date$stale';
  }

  @override
  String usageShares(String shares, String stale) {
    return 'This week’s usage: $shares$stale';
  }

  @override
  String get usageSheetNote =>
      'Usage updates as agents run on the computer. Cursor usage and Codex reset cards aren’t stored locally; the computer checks them after each one runs.';

  @override
  String usageShownPercent(String kind, String percent) {
    return '$kind $percent';
  }

  @override
  String usageStaleReading(String time) {
    return ' (reading from $time)';
  }

  @override
  String get usageTitle => 'Plan Usage';

  @override
  String get usageUsed => 'Used';

  @override
  String usageWindowDays(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count Days',
      one: '$count Day',
    );
    return '$_temp0';
  }

  @override
  String get usageWindowFiveHours => '5 Hours';

  @override
  String usageWindowHours(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count Hours',
      one: '$count Hour',
    );
    return '$_temp0';
  }

  @override
  String get usageWindowMonth => 'Monthly';

  @override
  String get usageWindowMonthShort => 'mo';

  @override
  String get usageWindowPeriod => 'Current Period';

  @override
  String get usageWindowWeek => 'Weekly';

  @override
  String get usageWindowWeekShort => 'wk';
}
