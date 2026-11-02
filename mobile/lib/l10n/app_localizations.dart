import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:intl/intl.dart' as intl;

import 'app_localizations_en.dart';
import 'app_localizations_zh.dart';

// ignore_for_file: type=lint

/// Callers can lookup localized strings with an instance of AppLocalizations
/// returned by `AppLocalizations.of(context)`.
///
/// Applications need to include `AppLocalizations.delegate()` in their app's
/// `localizationDelegates` list, and the locales they support in the app's
/// `supportedLocales` list. For example:
///
/// ```dart
/// import 'l10n/app_localizations.dart';
///
/// return MaterialApp(
///   localizationsDelegates: AppLocalizations.localizationsDelegates,
///   supportedLocales: AppLocalizations.supportedLocales,
///   home: MyApplicationHome(),
/// );
/// ```
///
/// ## Update pubspec.yaml
///
/// Please make sure to update your pubspec.yaml to include the following
/// packages:
///
/// ```yaml
/// dependencies:
///   # Internationalization support.
///   flutter_localizations:
///     sdk: flutter
///   intl: any # Use the pinned version from flutter_localizations
///
///   # Rest of dependencies
/// ```
///
/// ## iOS Applications
///
/// iOS applications define key application metadata, including supported
/// locales, in an Info.plist file that is built into the application bundle.
/// To configure the locales supported by your app, you’ll need to edit this
/// file.
///
/// First, open your project’s ios/Runner.xcworkspace Xcode workspace file.
/// Then, in the Project Navigator, open the Info.plist file under the Runner
/// project’s Runner folder.
///
/// Next, select the Information Property List item, select Add Item from the
/// Editor menu, then select Localizations from the pop-up menu.
///
/// Select and expand the newly-created Localizations item then, for each
/// locale your application supports, add a new item and select the locale
/// you wish to add from the pop-up menu in the Value field. This list should
/// be consistent with the languages listed in the AppLocalizations.supportedLocales
/// property.
abstract class AppLocalizations {
  AppLocalizations(String locale)
    : localeName = intl.Intl.canonicalizedLocale(locale.toString());

  final String localeName;

  static AppLocalizations of(BuildContext context) {
    return Localizations.of<AppLocalizations>(context, AppLocalizations)!;
  }

  static const LocalizationsDelegate<AppLocalizations> delegate =
      _AppLocalizationsDelegate();

  /// A list of this localizations delegate along with the default localizations
  /// delegates.
  ///
  /// Returns a list of localizations delegates containing this delegate along with
  /// GlobalMaterialLocalizations.delegate, GlobalCupertinoLocalizations.delegate,
  /// and GlobalWidgetsLocalizations.delegate.
  ///
  /// Additional delegates can be added by appending to this list in
  /// MaterialApp. This list does not have to be used at all if a custom list
  /// of delegates is preferred or required.
  static const List<LocalizationsDelegate<dynamic>> localizationsDelegates =
      <LocalizationsDelegate<dynamic>>[
        delegate,
        GlobalMaterialLocalizations.delegate,
        GlobalCupertinoLocalizations.delegate,
        GlobalWidgetsLocalizations.delegate,
      ];

  /// A list of this localizations delegate's supported locales.
  static const List<Locale> supportedLocales = <Locale>[
    Locale('en'),
    Locale('zh'),
  ];

  /// Common: refresh button or tooltip.
  ///
  /// In en, this message translates to:
  /// **'Refresh'**
  String get commonRefresh;

  /// Common: button that restores the default value.
  ///
  /// In en, this message translates to:
  /// **'Reset to Default'**
  String get commonResetToDefault;

  /// Common: button that tries again.
  ///
  /// In en, this message translates to:
  /// **'Retry'**
  String get commonRetry;

  /// Remote Desktop › connection settings sheet: error.
  ///
  /// In en, this message translates to:
  /// **'Enter an address first'**
  String get deskAddressRequired;

  /// Remote Desktop: short note over the picture.
  ///
  /// In en, this message translates to:
  /// **'The Mac is already unlocked. The password wasn’t sent.'**
  String get deskAlreadyUnlocked;

  /// Remote Desktop: floating bar button tooltip that leaves the page.
  ///
  /// In en, this message translates to:
  /// **'Back'**
  String get deskBack;

  /// Remote Desktop: banner when capturing the Mac screen failed.
  ///
  /// In en, this message translates to:
  /// **'Capture failed'**
  String get deskCaptureFailed;

  /// Remote Desktop: lock-screen pill key button tooltip.
  ///
  /// In en, this message translates to:
  /// **'Change or Forget Password'**
  String get deskChangePasswordTip;

  /// Remote Desktop: banner while connecting.
  ///
  /// In en, this message translates to:
  /// **'Connecting…'**
  String get deskConnecting;

  /// Remote Desktop › connection settings sheet: explanation.
  ///
  /// In en, this message translates to:
  /// **'By default the picture goes to the connected computer by subscription, and the computer picks the port. This changes the UDP address, for debugging.'**
  String get deskConnectionNote;

  /// Remote Desktop › connection settings sheet: title.
  ///
  /// In en, this message translates to:
  /// **'Connection Settings'**
  String get deskConnectionSettings;

  /// Remote Desktop: floating bar button tooltip that shows the debug panel.
  ///
  /// In en, this message translates to:
  /// **'Debug'**
  String get deskDebug;

  /// Remote Desktop › debug panel: decode time.
  ///
  /// In en, this message translates to:
  /// **'Decode {ms} ms'**
  String deskDebugDecode(String ms);

  /// Remote Desktop › debug panel: first line; fps is the shown frame rate label.
  ///
  /// In en, this message translates to:
  /// **'{fps}  received {received} fps'**
  String deskDebugHeadline(String fps, String received);

  /// Remote Desktop › debug panel: input sequence numbers.
  ///
  /// In en, this message translates to:
  /// **'Input: sent {sent}, acked {acked}'**
  String deskDebugInput(String sent, String acked);

  /// Remote Desktop › debug panel: keyframe recoveries.
  ///
  /// In en, this message translates to:
  /// **'Keyframes {count}'**
  String deskDebugKeyframes(String count);

  /// Remote Desktop › debug panel: lost packets.
  ///
  /// In en, this message translates to:
  /// **'Loss {loss}'**
  String deskDebugLoss(String loss);

  /// Remote Desktop › debug panel: network path.
  ///
  /// In en, this message translates to:
  /// **'Path {path}'**
  String deskDebugPath(String path);

  /// Remote Desktop › debug panel: bitrate line.
  ///
  /// In en, this message translates to:
  /// **'Bitrate {bitrate} ({bytes})  peak {peak}'**
  String deskDebugRate(String bitrate, String bytes, String peak);

  /// Remote Desktop › debug panel: viewport.
  ///
  /// In en, this message translates to:
  /// **'Zoom {zoom}  pan {x}, {y}'**
  String deskDebugView(String zoom, String x, String y);

  /// Remote Desktop: floating bar button tooltip.
  ///
  /// In en, this message translates to:
  /// **'Disconnect'**
  String get deskDisconnect;

  /// Remote Desktop: banner after the user disconnected.
  ///
  /// In en, this message translates to:
  /// **'Disconnected'**
  String get deskDisconnected;

  /// Remote Desktop › screen picker: name of another display, numbered left to right.
  ///
  /// In en, this message translates to:
  /// **'Display {number}'**
  String deskDisplayNumbered(String number);

  /// Remote Desktop: banner when viewing ended for any other reason.
  ///
  /// In en, this message translates to:
  /// **'Connection ended'**
  String get deskEndGeneric;

  /// Remote Desktop: banner when the Mac ended the stream because the phone’s receive reports stopped arriving.
  ///
  /// In en, this message translates to:
  /// **'The computer received no reports from the phone'**
  String get deskEndNoReport;

  /// Remote Desktop: banner when viewing was stopped from the Mac.
  ///
  /// In en, this message translates to:
  /// **'Stopped on the Mac'**
  String get deskEndStopped;

  /// Remote Desktop: banner when this phone stopped viewing.
  ///
  /// In en, this message translates to:
  /// **'Stopped viewing'**
  String get deskEndUnsubscribed;

  /// Remote Desktop: lock-screen pill button when no password is saved.
  ///
  /// In en, this message translates to:
  /// **'Enter Password'**
  String get deskEnterPassword;

  /// Remote Desktop › screen picker: section title for whole displays.
  ///
  /// In en, this message translates to:
  /// **'Entire Screen'**
  String get deskEntireScreen;

  /// Remote Desktop › debug panel: input log line; point is “x, y”.
  ///
  /// In en, this message translates to:
  /// **'Click {point}'**
  String deskEventClick(String point);

  /// Remote Desktop › debug panel: input log line for a multi-click; point is “x, y”.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} click} other {{count} clicks}} {point}'**
  String deskEventClicks(int count, String point);

  /// Remote Desktop › debug panel: input log line when a drag presses.
  ///
  /// In en, this message translates to:
  /// **'Down {point}'**
  String deskEventDown(String point);

  /// Remote Desktop › debug panel: input log line while dragging.
  ///
  /// In en, this message translates to:
  /// **'Drag {point}'**
  String deskEventMove(String point);

  /// Remote Desktop › debug panel: input log line when the pointer moves.
  ///
  /// In en, this message translates to:
  /// **'Point {point}'**
  String deskEventPoint(String point);

  /// Remote Desktop › debug panel: input log line.
  ///
  /// In en, this message translates to:
  /// **'Right-click {point}'**
  String deskEventRightClick(String point);

  /// Remote Desktop › debug panel: input log line.
  ///
  /// In en, this message translates to:
  /// **'Scroll {dx}, {dy}'**
  String deskEventScroll(String dx, String dy);

  /// Remote Desktop › debug panel: input log line for an inertia scroll.
  ///
  /// In en, this message translates to:
  /// **'Scroll {dx}, {dy} inertia'**
  String deskEventScrollInertia(String dx, String dy);

  /// Remote Desktop › debug panel: input log line for typed text.
  ///
  /// In en, this message translates to:
  /// **'Text {text}'**
  String deskEventText(String text);

  /// Remote Desktop › debug panel: input log line when a drag releases.
  ///
  /// In en, this message translates to:
  /// **'Up {point}'**
  String deskEventUp(String point);

  /// Remote Desktop: banner when another phone is already watching this Mac.
  ///
  /// In en, this message translates to:
  /// **'Another phone is viewing'**
  String get deskFailBusy;

  /// Remote Desktop: banner when the Mac and the phone share no video codec.
  ///
  /// In en, this message translates to:
  /// **'This computer can’t encode video this phone can play'**
  String get deskFailCodec;

  /// Remote Desktop: banner when the connection to the computer dropped.
  ///
  /// In en, this message translates to:
  /// **'Connection lost'**
  String get deskFailDisconnected;

  /// Remote Desktop: banner for any other connection failure.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t connect'**
  String get deskFailGeneric;

  /// Remote Desktop: banner when the guessed LAN address doesn’t reach the Mac.
  ///
  /// In en, this message translates to:
  /// **'This phone and the Mac aren’t on the same local network'**
  String get deskFailLan;

  /// Remote Desktop: banner when this device has no desktop viewer support.
  ///
  /// In en, this message translates to:
  /// **'This device can’t view the desktop'**
  String get deskFailNoPlugin;

  /// Remote Desktop: banner when the computer’s Posse is too old to stream the desktop.
  ///
  /// In en, this message translates to:
  /// **'Posse on this computer needs an update'**
  String get deskFailOutdated;

  /// Remote Desktop: banner when neither the direct path nor the relay delivers the picture.
  ///
  /// In en, this message translates to:
  /// **'Neither a direct connection nor the relay works'**
  String get deskFailRoute;

  /// Remote Desktop: banner when Posse lacks the screen recording permission on the Mac.
  ///
  /// In en, this message translates to:
  /// **'Allow screen recording on the Mac'**
  String get deskFailScreenPermission;

  /// Remote Desktop › unlock sheet: button.
  ///
  /// In en, this message translates to:
  /// **'Forget Password'**
  String get deskForgetPassword;

  /// Remote Desktop: floating bar label in place of the frame rate when the picture isn’t changing.
  ///
  /// In en, this message translates to:
  /// **'Still'**
  String get deskFpsStill;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Click'**
  String get deskGestureClick;

  /// Remote Desktop › gesture help: note at the bottom.
  ///
  /// In en, this message translates to:
  /// **'Tap anywhere to close. Change the mode under Remote Desktop in Settings.'**
  String get deskGestureDismiss;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Drag'**
  String get deskGestureDrag;

  /// Remote Desktop › gesture help: how to drag in direct touch mode.
  ///
  /// In en, this message translates to:
  /// **'Long-press until it buzzes, then drag'**
  String get deskGestureLongPressDrag;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Move Pointer'**
  String get deskGestureMovePointer;

  /// Remote Desktop › gesture help: action name for moving the zoomed-in picture.
  ///
  /// In en, this message translates to:
  /// **'Pan'**
  String get deskGesturePan;

  /// Remote Desktop › gesture help: how to zoom.
  ///
  /// In en, this message translates to:
  /// **'Pinch'**
  String get deskGesturePinch;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Right-Click'**
  String get deskGestureRightClick;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Scroll'**
  String get deskGestureScroll;

  /// Remote Desktop › gesture help: how to move the pointer in trackpad mode.
  ///
  /// In en, this message translates to:
  /// **'Slide one finger'**
  String get deskGestureSlideOne;

  /// Remote Desktop › gesture help: how to click in direct touch mode.
  ///
  /// In en, this message translates to:
  /// **'Tap one finger'**
  String get deskGestureTap;

  /// Remote Desktop › gesture help: how to click in trackpad mode.
  ///
  /// In en, this message translates to:
  /// **'Tap one finger; clicks at the pointer'**
  String get deskGestureTapAtPointer;

  /// Remote Desktop › gesture help: how to drag in trackpad mode.
  ///
  /// In en, this message translates to:
  /// **'Slide three fingers, or tap then hold and slide'**
  String get deskGestureThreeFingerDrag;

  /// Remote Desktop: title of the gesture help and tooltip of its button in the floating bar.
  ///
  /// In en, this message translates to:
  /// **'Gestures'**
  String get deskGestureTitle;

  /// Remote Desktop › gesture help: how to scroll.
  ///
  /// In en, this message translates to:
  /// **'Drag two fingers'**
  String get deskGestureTwoFingerDrag;

  /// Remote Desktop › gesture help: how to right-click.
  ///
  /// In en, this message translates to:
  /// **'Tap two fingers'**
  String get deskGestureTwoFingerTap;

  /// Remote Desktop › gesture help: action name.
  ///
  /// In en, this message translates to:
  /// **'Zoom'**
  String get deskGestureZoom;

  /// Remote Desktop › gesture help: how to pan in direct touch mode.
  ///
  /// In en, this message translates to:
  /// **'Drag one finger when zoomed in'**
  String get deskGestureZoomedDrag;

  /// Remote Desktop › connection settings sheet: field label.
  ///
  /// In en, this message translates to:
  /// **'Host'**
  String get deskHost;

  /// Remote Desktop: banner when the phone reached the Mac through a tunnel the video can’t use.
  ///
  /// In en, this message translates to:
  /// **'The picture can’t get through the tunnel. On the same Wi-Fi, scan this Mac’s LAN code, or use Tailscale.'**
  String get deskIndirect;

  /// Remote Desktop: floating bar button tooltip.
  ///
  /// In en, this message translates to:
  /// **'Keyboard'**
  String get deskKeyboard;

  /// Remote Desktop › screen picker: loading note.
  ///
  /// In en, this message translates to:
  /// **'Listing windows…'**
  String get deskListingWindows;

  /// Remote Desktop: banner and lock-screen pill label when the Mac is locked.
  ///
  /// In en, this message translates to:
  /// **'Mac is locked'**
  String get deskMacLocked;

  /// Remote Desktop › screen picker: name of the main display when macOS gives none.
  ///
  /// In en, this message translates to:
  /// **'Main Display'**
  String get deskMainDisplay;

  /// Remote Desktop › shortcut bar: key that opens Mission Control.
  ///
  /// In en, this message translates to:
  /// **'Mission Control'**
  String get deskMissionControl;

  /// Remote Desktop › gesture help: badge naming the direct touch input mode.
  ///
  /// In en, this message translates to:
  /// **'Direct Touch Mode'**
  String get deskModeDirect;

  /// Remote Desktop › gesture help: badge naming the trackpad input mode.
  ///
  /// In en, this message translates to:
  /// **'Trackpad Mode'**
  String get deskModeTrackpad;

  /// Remote Desktop: note over the picture when Posse lacks the Accessibility permission on the Mac.
  ///
  /// In en, this message translates to:
  /// **'View only. Turn on Accessibility for Posse on the Mac to control it.'**
  String get deskNoAccessibility;

  /// Remote Desktop: banner and connection settings note when no computer is connected.
  ///
  /// In en, this message translates to:
  /// **'No computer connected'**
  String get deskNoComputer;

  /// Remote Desktop: short note over the picture.
  ///
  /// In en, this message translates to:
  /// **'Saved password forgotten'**
  String get deskPasswordForgotten;

  /// Remote Desktop › unlock sheet: password field placeholder.
  ///
  /// In en, this message translates to:
  /// **'Login Password'**
  String get deskPasswordHint;

  /// Remote Desktop: short note over the picture.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t save the password'**
  String get deskPasswordSaveFailed;

  /// Remote Desktop: short note over the picture.
  ///
  /// In en, this message translates to:
  /// **'Password saved. Next time the Mac locks, tap “Unlock”.'**
  String get deskPasswordSaved;

  /// Remote Desktop › debug panel: the picture comes over the local network.
  ///
  /// In en, this message translates to:
  /// **'Direct'**
  String get deskPathLan;

  /// Remote Desktop › debug panel: the picture comes over a hole-punched direct UDP path.
  ///
  /// In en, this message translates to:
  /// **'Direct (Hole-Punched)'**
  String get deskPathPunched;

  /// Remote Desktop › debug panel: the picture comes through the relay.
  ///
  /// In en, this message translates to:
  /// **'Relay'**
  String get deskPathRelay;

  /// Remote Desktop: banner while the app is in the background.
  ///
  /// In en, this message translates to:
  /// **'Paused (in background)'**
  String get deskPaused;

  /// Remote Desktop › connection settings sheet: field label.
  ///
  /// In en, this message translates to:
  /// **'Port'**
  String get deskPort;

  /// Remote Desktop › connection settings sheet: error.
  ///
  /// In en, this message translates to:
  /// **'Port must be from 1 to 65535'**
  String get deskPortRange;

  /// Remote Desktop: floating bar tooltip and banner button after disconnecting.
  ///
  /// In en, this message translates to:
  /// **'Reconnect'**
  String get deskReconnect;

  /// Remote Desktop › unlock sheet: checkbox chip.
  ///
  /// In en, this message translates to:
  /// **'Remember'**
  String get deskRemember;

  /// Remote Desktop › screen picker: resolution of the main display.
  ///
  /// In en, this message translates to:
  /// **'{width} × {height} · Main'**
  String deskResolutionMain(String width, String height);

  /// Remote Desktop: hint under the screen recording permission banner.
  ///
  /// In en, this message translates to:
  /// **'On the Mac, turn on Posse in System Settings › Privacy & Security › Screen & System Audio Recording, then retry.'**
  String get deskScreenPermissionHint;

  /// Remote Desktop: floating bar button tooltip that shows the shortcut key bar.
  ///
  /// In en, this message translates to:
  /// **'Shortcuts'**
  String get deskShortcuts;

  /// Remote Desktop › shortcut bar: key that moves one space left.
  ///
  /// In en, this message translates to:
  /// **'◀ Space'**
  String get deskSpaceLeft;

  /// Remote Desktop › shortcut bar: key that moves one space right.
  ///
  /// In en, this message translates to:
  /// **'Space ▶'**
  String get deskSpaceRight;

  /// Remote Desktop: floating bar button tooltip.
  ///
  /// In en, this message translates to:
  /// **'Switch to Landscape'**
  String get deskToLandscape;

  /// Remote Desktop: floating bar button tooltip.
  ///
  /// In en, this message translates to:
  /// **'Switch to Portrait'**
  String get deskToPortrait;

  /// Remote Desktop › debug panel: button that opens connection settings.
  ///
  /// In en, this message translates to:
  /// **'UDP Address'**
  String get deskUdpAddress;

  /// Remote Desktop: button that unlocks the Mac (lock-screen pill and unlock sheet).
  ///
  /// In en, this message translates to:
  /// **'Unlock'**
  String get deskUnlock;

  /// Remote Desktop › unlock sheet: explanation.
  ///
  /// In en, this message translates to:
  /// **'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed.'**
  String get deskUnlockNote;

  /// Remote Desktop › unlock sheet: explanation when the password can be remembered.
  ///
  /// In en, this message translates to:
  /// **'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed. “Remember” stores it on this phone only.'**
  String get deskUnlockNoteRemember;

  /// Remote Desktop › connection settings sheet: button that applies the address.
  ///
  /// In en, this message translates to:
  /// **'Use'**
  String get deskUse;

  /// Remote Desktop: floating bar button tooltip that opens the screen and window picker.
  ///
  /// In en, this message translates to:
  /// **'Choose Screen or Window'**
  String get deskViewTip;

  /// Remote Desktop: banner while waiting for the first frame.
  ///
  /// In en, this message translates to:
  /// **'Connected. Waiting for the picture…'**
  String get deskWaitingPicture;

  /// Remote Desktop: short note over the picture when the watched window disappears.
  ///
  /// In en, this message translates to:
  /// **'The window closed or left this space. Showing the entire screen.'**
  String get deskWindowGone;

  /// Remote Desktop › screen picker: section title for single windows.
  ///
  /// In en, this message translates to:
  /// **'Windows'**
  String get deskWindows;

  /// Remote Desktop › screen picker: empty note.
  ///
  /// In en, this message translates to:
  /// **'No windows in this space can be viewed on their own'**
  String get deskWindowsEmpty;

  /// Remote Desktop › screen picker: error note.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t list windows. Try again later.'**
  String get deskWindowsError;

  /// Settings › Language: option that follows the phone's language.
  ///
  /// In en, this message translates to:
  /// **'System'**
  String get settingsLanguageSystem;

  /// Settings: title of the section that picks the app language.
  ///
  /// In en, this message translates to:
  /// **'Language'**
  String get settingsLanguageTitle;

  /// Plan Usage: Cursor’s Auto pool, named as in its dashboard.
  ///
  /// In en, this message translates to:
  /// **'Cursor Models'**
  String get usageCursorModels;

  /// Plan Usage sheet: money used of the included amount.
  ///
  /// In en, this message translates to:
  /// **'Included usage {used} / {limit} ({percent})'**
  String usageIncludedSpend(String used, String limit, String percent);

  /// Plan Usage sheet: separator between items in usageShares.
  ///
  /// In en, this message translates to:
  /// **', '**
  String get usageListSeparator;

  /// Plan Usage sheet: a pool’s window, e.g. “Fable Weekly”.
  ///
  /// In en, this message translates to:
  /// **'{name} {window}'**
  String usageNamedWindow(String name, String window);

  /// Plan Usage sheet: error when refreshing without a connection.
  ///
  /// In en, this message translates to:
  /// **'The computer isn’t connected'**
  String get usageNotConnected;

  /// Plan Usage sheet: error when the computer can’t refresh usage.
  ///
  /// In en, this message translates to:
  /// **'Posse on the computer is too old. Update it to refresh.'**
  String get usageOldPosse;

  /// Plan Usage sheet: on-demand money spent; stale is usageStaleReading or empty.
  ///
  /// In en, this message translates to:
  /// **'On-demand usage {used}{stale}'**
  String usageOnDemand(String used, String stale);

  /// Plan Usage sheet: on-demand money spent with a limit; stale is usageStaleReading or empty.
  ///
  /// In en, this message translates to:
  /// **'On-demand usage {used}, limit {limit}{stale}'**
  String usageOnDemandLimited(String used, String limit, String stale);

  /// Plan Usage: Cursor’s API pool, named as in its dashboard.
  ///
  /// In en, this message translates to:
  /// **'Other Models'**
  String get usageOtherModels;

  /// Plan Usage: replaces “/mo” in a plan’s monthly price.
  ///
  /// In en, this message translates to:
  /// **'/mo'**
  String get usagePerMonth;

  /// Plan Usage sheet: reset time plus the age of a pool’s own reading.
  ///
  /// In en, this message translates to:
  /// **'{reset}, reading from {time}'**
  String usageReadingAge(String reset, String time);

  /// Plan Usage sheet: segmented option showing remaining percentages.
  ///
  /// In en, this message translates to:
  /// **'Remaining'**
  String get usageRemaining;

  /// Plan Usage sheet: Codex reset cards.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} reset card} other {{count} reset cards}}{stale}'**
  String usageResetCards(int count, String stale);

  /// Plan Usage sheet: Codex reset cards with the earliest expiry date.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} reset card} other {{count} reset cards}}, earliest expires {date}{stale}'**
  String usageResetCardsExpiring(int count, String date, String stale);

  /// Plan Usage sheet: where this week’s Claude usage went; shares is a list like “Claude Code 92%”.
  ///
  /// In en, this message translates to:
  /// **'This week’s usage: {shares}{stale}'**
  String usageShares(String shares, String stale);

  /// Plan Usage sheet: note at the bottom.
  ///
  /// In en, this message translates to:
  /// **'Usage updates as agents run on the computer. Cursor usage and Codex reset cards aren’t stored locally; the computer checks them after each one runs.'**
  String get usageSheetNote;

  /// Plan Usage sheet: percentage after a bar; kind is Used or Remaining, percent includes %.
  ///
  /// In en, this message translates to:
  /// **'{kind} {percent}'**
  String usageShownPercent(String kind, String percent);

  /// Plan Usage sheet: appended to a fact from an older reading; time is a relative time.
  ///
  /// In en, this message translates to:
  /// **' (reading from {time})'**
  String usageStaleReading(String time);

  /// Plan Usage sheet: title.
  ///
  /// In en, this message translates to:
  /// **'Plan Usage'**
  String get usageTitle;

  /// Plan Usage sheet: segmented option showing used percentages.
  ///
  /// In en, this message translates to:
  /// **'Used'**
  String get usageUsed;

  /// Plan Usage: limit window measured in days.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} Day} other {{count} Days}}'**
  String usageWindowDays(int count);

  /// Plan Usage: five-hour limit window.
  ///
  /// In en, this message translates to:
  /// **'5 Hours'**
  String get usageWindowFiveHours;

  /// Plan Usage: limit window measured in hours.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} Hour} other {{count} Hours}}'**
  String usageWindowHours(int count);

  /// Plan Usage: monthly limit window.
  ///
  /// In en, this message translates to:
  /// **'Monthly'**
  String get usageWindowMonth;

  /// Plan Usage card: short name of the monthly window.
  ///
  /// In en, this message translates to:
  /// **'mo'**
  String get usageWindowMonthShort;

  /// Plan Usage: limit window of unknown length.
  ///
  /// In en, this message translates to:
  /// **'Current Period'**
  String get usageWindowPeriod;

  /// Plan Usage: weekly limit window.
  ///
  /// In en, this message translates to:
  /// **'Weekly'**
  String get usageWindowWeek;

  /// Plan Usage card: short name of the weekly window.
  ///
  /// In en, this message translates to:
  /// **'wk'**
  String get usageWindowWeekShort;
}

class _AppLocalizationsDelegate
    extends LocalizationsDelegate<AppLocalizations> {
  const _AppLocalizationsDelegate();

  @override
  Future<AppLocalizations> load(Locale locale) {
    return SynchronousFuture<AppLocalizations>(lookupAppLocalizations(locale));
  }

  @override
  bool isSupported(Locale locale) =>
      <String>['en', 'zh'].contains(locale.languageCode);

  @override
  bool shouldReload(_AppLocalizationsDelegate old) => false;
}

AppLocalizations lookupAppLocalizations(Locale locale) {
  // Lookup logic when only language code is specified.
  switch (locale.languageCode) {
    case 'en':
      return AppLocalizationsEn();
    case 'zh':
      return AppLocalizationsZh();
  }

  throw FlutterError(
    'AppLocalizations.delegate failed to load unsupported locale "$locale". This is likely '
    'an issue with the localizations generation tool. Please file an issue '
    'on GitHub with a reproducible sample app and the gen-l10n configuration '
    'that was used.',
  );
}
