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

  /// Chat: permission card button that allows the tool call.
  ///
  /// In en, this message translates to:
  /// **'Allow'**
  String get chatAllow;

  /// Chat: title of the permission card when the agent asks to run a tool. {tool} is the tool name.
  ///
  /// In en, this message translates to:
  /// **'Allow {tool}?'**
  String chatAllowTitle(String tool);

  /// Chat: brief notice after tapping Paste when the clipboard is empty.
  ///
  /// In en, this message translates to:
  /// **'There’s no text or image on the clipboard'**
  String get chatClipboardEmpty;

  /// Chat and Markdown: header of a code block that names no language.
  ///
  /// In en, this message translates to:
  /// **'Code'**
  String get chatCode;

  /// Chat: body of the compact-context dialog. {percent} is the used percentage, {agent} the agent name.
  ///
  /// In en, this message translates to:
  /// **'{percent}% of the context is used. {agent} will condense the conversation so far into a summary (/compact) to make room to keep going.'**
  String chatCompactBody(int percent, String agent);

  /// Chat: confirm button of the compact-context dialog.
  ///
  /// In en, this message translates to:
  /// **'Compact'**
  String get chatCompactButton;

  /// Chat: title of the dialog shown after tapping the context meter.
  ///
  /// In en, this message translates to:
  /// **'Compact Context?'**
  String get chatCompactConfirm;

  /// Chat: tooltip of the green dot on the agent icon.
  ///
  /// In en, this message translates to:
  /// **'Connected to the Mac'**
  String get chatConnected;

  /// Chat: status line while the phone connects to the computer.
  ///
  /// In en, this message translates to:
  /// **'Connecting to the Mac…'**
  String get chatConnecting;

  /// Chat: accessibility label of the context meter in the status line.
  ///
  /// In en, this message translates to:
  /// **'{percent}% of context used'**
  String chatContextUsed(int percent);

  /// Chat: permission card button that denies the tool call.
  ///
  /// In en, this message translates to:
  /// **'Deny'**
  String get chatDeny;

  /// Chat: tooltip of the amber dot on the agent icon.
  ///
  /// In en, this message translates to:
  /// **'Disconnected from the Mac'**
  String get chatDisconnected;

  /// Chat: button at the top of the conversation that loads older messages.
  ///
  /// In en, this message translates to:
  /// **'Earlier Messages'**
  String get chatEarlierMessages;

  /// Chat: shown in place of the message box after the session ended.
  ///
  /// In en, this message translates to:
  /// **'This session has ended; its transcript is kept. To keep going, start a new session from Home or restore it from History on the Mac.'**
  String get chatEndedNote;

  /// Chat and Terminal: error when uploading an empty file.
  ///
  /// In en, this message translates to:
  /// **'The file is empty'**
  String get chatFileEmpty;

  /// Chat and Terminal: error when the computer refused to start a file upload.
  ///
  /// In en, this message translates to:
  /// **'The Mac didn’t accept this file'**
  String get chatFileNotAccepted;

  /// Chat and Terminal: error when an old computer can’t take a large upload.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old to receive a file this large. Update Posse on the Mac first.'**
  String get chatFileTooLargeForOldPosse;

  /// Chat: placeholder for an image in a message that has no path or URL.
  ///
  /// In en, this message translates to:
  /// **'Image'**
  String get chatImage;

  /// Chat: line in a just-sent message saying how many images it carries.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} image} other {{count} images}}'**
  String chatImageCount(int count);

  /// Chat and Terminal: error when uploading an empty image.
  ///
  /// In en, this message translates to:
  /// **'The image is empty'**
  String get chatImageEmpty;

  /// Chat: brief notice when the user picks more images than one message can carry.
  ///
  /// In en, this message translates to:
  /// **'Up to {count, plural, one {{count} image} other {{count} images}} at a time'**
  String chatImageLimit(int count);

  /// Chat and Terminal: error when the computer refused to start an image upload.
  ///
  /// In en, this message translates to:
  /// **'The Mac didn’t accept this image'**
  String get chatImageNotAccepted;

  /// Chat and Terminal: error when the computer returned no path for an uploaded image.
  ///
  /// In en, this message translates to:
  /// **'The Mac didn’t save this image'**
  String get chatImageNotSaved;

  /// Accessibility label of the latency reading. {ms} is the round-trip time in milliseconds.
  ///
  /// In en, this message translates to:
  /// **'Latency to the Mac: {ms} ms'**
  String chatLatencyLabel(int ms);

  /// Status line: short text next to the empty signal icon when pings time out.
  ///
  /// In en, this message translates to:
  /// **'No Response'**
  String get chatLatencyUnresponsive;

  /// Accessibility label of the latency reading when pings time out.
  ///
  /// In en, this message translates to:
  /// **'The Mac isn’t responding'**
  String get chatLatencyUnresponsiveLabel;

  /// Images & Videos: name of an item produced by code (e.g. a notebook cell) that has no file name.
  ///
  /// In en, this message translates to:
  /// **'Code output'**
  String get chatMediaCodeOutput;

  /// Images & Videos: name of an item the agent generated that has no file name.
  ///
  /// In en, this message translates to:
  /// **'Generated image'**
  String get chatMediaGenerated;

  /// Images & Videos: accessibility label of an image tile. {name} is the item name.
  ///
  /// In en, this message translates to:
  /// **'Image {name}'**
  String chatMediaImageLabel(String name);

  /// Images & Videos: section header for images and videos the agent generated.
  ///
  /// In en, this message translates to:
  /// **'Generated by Agent'**
  String get chatMediaMadeHeader;

  /// Images & Videos: empty state.
  ///
  /// In en, this message translates to:
  /// **'This session has no images or videos yet'**
  String get chatMediaNone;

  /// Images & Videos: error when the computer doesn’t support listing media.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old. Update it to see the session’s images and videos on your phone.'**
  String get chatMediaOldPosse;

  /// Images & Videos: name of an image the user pasted into the session that has no file name.
  ///
  /// In en, this message translates to:
  /// **'Pasted'**
  String get chatMediaPasted;

  /// Images & Videos: section header for images the user pasted.
  ///
  /// In en, this message translates to:
  /// **'Pasted'**
  String get chatMediaPastedHeader;

  /// Images & Videos: note under the generated section when older items are left out.
  ///
  /// In en, this message translates to:
  /// **'Showing only the {count, plural, one {most recent item} other {{count} most recent items}}'**
  String chatMediaRecentOnly(int count);

  /// Images & Videos: page title, opened from the session’s “…” menu.
  ///
  /// In en, this message translates to:
  /// **'Images & Videos'**
  String get chatMediaTitle;

  /// Images & Videos: accessibility label of a video tile. {name} is the item name.
  ///
  /// In en, this message translates to:
  /// **'Video {name}'**
  String chatMediaVideoLabel(String name);

  /// Chat: floating button that jumps to new messages below.
  ///
  /// In en, this message translates to:
  /// **'New Messages'**
  String get chatNewMessages;

  /// Chat and Terminal: error when the camera permission was denied.
  ///
  /// In en, this message translates to:
  /// **'Posse can’t use the camera. Allow camera access for Posse in Settings.'**
  String get chatNoCameraAccess;

  /// Chat and Terminal: error when the photo library permission was denied.
  ///
  /// In en, this message translates to:
  /// **'Posse can’t access your photos. Allow photo access for Posse in Settings.'**
  String get chatNoPhotoAccess;

  /// Chat: shown instead of the conversation when the agent keeps no session file.
  ///
  /// In en, this message translates to:
  /// **'This session has no transcript'**
  String get chatNoTranscript;

  /// Chat: placeholder of the free-text field under the options of a question the agent asks.
  ///
  /// In en, this message translates to:
  /// **'Other answer'**
  String get chatOtherAnswer;

  /// Chat: item in the attach sheet that pastes an image or text from the clipboard.
  ///
  /// In en, this message translates to:
  /// **'Paste from Clipboard'**
  String get chatPasteFromClipboard;

  /// Chat and Terminal: item in the attach sheet that opens the system Files app.
  ///
  /// In en, this message translates to:
  /// **'Choose from Files'**
  String get chatPickFromFiles;

  /// Terminal: item in the send-file sheet that opens the photo library.
  ///
  /// In en, this message translates to:
  /// **'Choose a Photo or Video'**
  String get chatPickFromLibrary;

  /// Chat: item in the attach sheet that opens the photo library with multiple selection.
  ///
  /// In en, this message translates to:
  /// **'Choose Photos or Videos (Multiple Allowed)'**
  String get chatPickFromLibraryMulti;

  /// Chat: notice when the session stream reports an error without a message.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t read the session'**
  String get chatReadFailed;

  /// Chat: status line after the connection to the computer dropped.
  ///
  /// In en, this message translates to:
  /// **'Disconnected, reconnecting…'**
  String get chatReconnecting;

  /// Chat: tooltip of the × on an image attached to the message box.
  ///
  /// In en, this message translates to:
  /// **'Remove Image'**
  String get chatRemoveImage;

  /// Chat: placeholder of the message box. {agent} is the agent name, e.g. Claude.
  ///
  /// In en, this message translates to:
  /// **'Reply to {agent}'**
  String chatReplyHint(String agent);

  /// Chat: sender name above the agent’s reasoning blocks.
  ///
  /// In en, this message translates to:
  /// **'Thinking'**
  String get chatRoleReasoning;

  /// Chat: sender name above system messages.
  ///
  /// In en, this message translates to:
  /// **'System'**
  String get chatRoleSystem;

  /// Chat: sender name for tool messages; also the fallback title of a tool call with no name.
  ///
  /// In en, this message translates to:
  /// **'Tool'**
  String get chatRoleTool;

  /// Chat: sender name above the user’s own messages.
  ///
  /// In en, this message translates to:
  /// **'You'**
  String get chatRoleYou;

  /// Status line after the latency reading: the connection is direct (LAN, Tailscale, hole punching).
  ///
  /// In en, this message translates to:
  /// **'Direct'**
  String get chatRouteDirect;

  /// Status line after the latency reading: the connection goes through the relay.
  ///
  /// In en, this message translates to:
  /// **'Relay'**
  String get chatRouteRelay;

  /// Chat: tooltip of the send button in the message box.
  ///
  /// In en, this message translates to:
  /// **'Send'**
  String get chatSend;

  /// Chat: tooltip of the image button in the message box.
  ///
  /// In en, this message translates to:
  /// **'Send Image'**
  String get chatSendImage;

  /// Chat and Terminal: brief notice when the computer refused to write the message into the terminal.
  ///
  /// In en, this message translates to:
  /// **'Not delivered'**
  String get chatSendRefused;

  /// Chat and Terminal: brief notice when it’s unknown whether the message reached the terminal.
  ///
  /// In en, this message translates to:
  /// **'Not confirmed. Check the chat before retrying.'**
  String get chatSendUnknown;

  /// Chat: status next to a message the user just sent, while it is being sent.
  ///
  /// In en, this message translates to:
  /// **'Sending…'**
  String get chatSending;

  /// Chat: status next to a message the user just sent, once the computer accepted it.
  ///
  /// In en, this message translates to:
  /// **'Sent'**
  String get chatSent;

  /// Chat: status line when the agent finished its turn.
  ///
  /// In en, this message translates to:
  /// **'Turn Finished'**
  String get chatStateDone;

  /// Chat: status line under the session title after the session ended.
  ///
  /// In en, this message translates to:
  /// **'Session ended'**
  String get chatStateEnded;

  /// Chat: status line while the agent starts up.
  ///
  /// In en, this message translates to:
  /// **'Starting'**
  String get chatStateStarting;

  /// Chat: status line while the agent waits for agents it dispatched.
  ///
  /// In en, this message translates to:
  /// **'Waiting on Tasks'**
  String get chatStateTasks;

  /// Chat: status line while the agent runs a tool. {tool} is the tool name.
  ///
  /// In en, this message translates to:
  /// **'Using {tool}'**
  String chatStateUsingTool(String tool);

  /// Chat: status line when the agent waits for the user’s approval or answer.
  ///
  /// In en, this message translates to:
  /// **'Needs You'**
  String get chatStateWaiting;

  /// Chat: status line while the agent is working.
  ///
  /// In en, this message translates to:
  /// **'Working'**
  String get chatStateWorking;

  /// Chat: notice when the computer closed the session stream.
  ///
  /// In en, this message translates to:
  /// **'The session stream ended'**
  String get chatStreamEnded;

  /// Chat: label next to the agent name while its reply is still streaming.
  ///
  /// In en, this message translates to:
  /// **'Writing'**
  String get chatStreaming;

  /// Chat: button that sends the answers to the agent’s questions.
  ///
  /// In en, this message translates to:
  /// **'Submit'**
  String get chatSubmitAnswer;

  /// Chat: item in the “…” menu that shows the session as a terminal.
  ///
  /// In en, this message translates to:
  /// **'Switch to Terminal'**
  String get chatSwitchToTerminal;

  /// Chat and Terminal: item in the attach sheet that opens the camera.
  ///
  /// In en, this message translates to:
  /// **'Take Photo'**
  String get chatTakePhoto;

  /// Chat: status line for a plain terminal session with no agent.
  ///
  /// In en, this message translates to:
  /// **'Terminal'**
  String get chatTerminal;

  /// Chat: stands in for the tool name in “Allow {tool}?” when the agent didn’t say which tool; lowercase in English.
  ///
  /// In en, this message translates to:
  /// **'this action'**
  String get chatThisAction;

  /// Chat: collapsed row summarizing consecutive tool calls; tap to expand.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} tool call} other {{count} tool calls}}'**
  String chatToolCalls(int count);

  /// Chat: title of a tool result that returned an error.
  ///
  /// In en, this message translates to:
  /// **'Tool Failed'**
  String get chatToolFailed;

  /// Skills & Tools: group header for command-line programs that are neither from Homebrew nor built in.
  ///
  /// In en, this message translates to:
  /// **'Other Commands'**
  String get chatToolsCliOther;

  /// Skills & Tools: group header for command-line programs that ship with the system.
  ///
  /// In en, this message translates to:
  /// **'Built-in'**
  String get chatToolsCliSystem;

  /// Skills & Tools: brief notice after tapping a name to copy it.
  ///
  /// In en, this message translates to:
  /// **'Copied {name}'**
  String chatToolsCopied(String name);

  /// Skills & Tools: button in the sheet showing a full command line.
  ///
  /// In en, this message translates to:
  /// **'Copy Command'**
  String get chatToolsCopyCommand;

  /// Skills & Tools: button in the sheet showing a full URL.
  ///
  /// In en, this message translates to:
  /// **'Copy URL'**
  String get chatToolsCopyUrl;

  /// Skills & Tools: empty state before the agent wrote its session file.
  ///
  /// In en, this message translates to:
  /// **'No session file yet. It appears once the agent starts a conversation.'**
  String get chatToolsNoFile;

  /// Skills & Tools: empty state of the Skills & MCP filter.
  ///
  /// In en, this message translates to:
  /// **'This session hasn’t used any skills or MCP yet'**
  String get chatToolsNoSkills;

  /// Skills & Tools: empty state of the URLs filter.
  ///
  /// In en, this message translates to:
  /// **'This session hasn’t visited any URLs yet'**
  String get chatToolsNoWeb;

  /// Skills & Tools: empty state.
  ///
  /// In en, this message translates to:
  /// **'This session hasn’t used any skills, MCP, command-line tools or URLs yet'**
  String get chatToolsNone;

  /// Skills & Tools: error when the computer doesn’t support the page.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old. Update it to see the session’s skills and tools on your phone.'**
  String get chatToolsOldPosse;

  /// Skills & Tools: button in the sheet showing a full URL.
  ///
  /// In en, this message translates to:
  /// **'Open in Browser'**
  String get chatToolsOpenInBrowser;

  /// Skills & Tools: kind shown under a web search in the timeline. {via} is the tool that searched, or “Sent by you”.
  ///
  /// In en, this message translates to:
  /// **'Search · {via}'**
  String chatToolsSearchVia(String via);

  /// Skills & Tools: kind shown under an MCP call that failed. {server} is the MCP server name.
  ///
  /// In en, this message translates to:
  /// **'{server} · Failed'**
  String chatToolsServerFailed(String server);

  /// Skills & Tools: kind shown under a skill call in the timeline.
  ///
  /// In en, this message translates to:
  /// **'Skill'**
  String get chatToolsSkill;

  /// Skills & Tools: filter option showing only skills and MCP servers.
  ///
  /// In en, this message translates to:
  /// **'Skills & MCP'**
  String get chatToolsSkillsMcp;

  /// Skills & Tools: section header of the list of calls, newest first.
  ///
  /// In en, this message translates to:
  /// **'Timeline'**
  String get chatToolsTimeline;

  /// Skills & Tools: page title, opened from the session’s “…” menu.
  ///
  /// In en, this message translates to:
  /// **'Skills & Tools'**
  String get chatToolsTitle;

  /// Skills & Tools: source of a URL the user sent in a message (otherwise the tool or program name is shown).
  ///
  /// In en, this message translates to:
  /// **'Sent by you'**
  String get chatToolsViaUser;

  /// Skills & Tools: filter option and section header for visited websites.
  ///
  /// In en, this message translates to:
  /// **'URLs'**
  String get chatToolsWeb;

  /// Skills & Tools: section header for websites when the agent also ran web searches; two spaces separate the parts.
  ///
  /// In en, this message translates to:
  /// **'URLs  {count, plural, one {{count} search} other {{count} searches}}'**
  String chatToolsWebSearches(int count);

  /// Chat: last row of the conversation while the agent runs a tool. {tool} is the tool name.
  ///
  /// In en, this message translates to:
  /// **'Using {tool}…'**
  String chatUsingTool(String tool);

  /// Chat: last row of the conversation while the agent works and no status line is known.
  ///
  /// In en, this message translates to:
  /// **'Working…'**
  String get chatWorking;

  /// Filter option that shows everything.
  ///
  /// In en, this message translates to:
  /// **'All'**
  String get commonAll;

  /// Button: cancel a dialog.
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get commonCancel;

  /// Button or tooltip: close.
  ///
  /// In en, this message translates to:
  /// **'Close'**
  String get commonClose;

  /// Button: copy the selection.
  ///
  /// In en, this message translates to:
  /// **'Copy'**
  String get commonCopy;

  /// Button: delete.
  ///
  /// In en, this message translates to:
  /// **'Delete'**
  String get commonDelete;

  /// Accessibility label of a loading spinner.
  ///
  /// In en, this message translates to:
  /// **'Loading'**
  String get commonLoading;

  /// Tooltip of the “more” (…) button in a top bar.
  ///
  /// In en, this message translates to:
  /// **'More'**
  String get commonMore;

  /// Button: paste the clipboard.
  ///
  /// In en, this message translates to:
  /// **'Paste'**
  String get commonPaste;

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

  /// Button: save.
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get commonSave;

  /// Button: select all.
  ///
  /// In en, this message translates to:
  /// **'Select All'**
  String get commonSelectAll;

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

  /// Remote Desktop: the unlock button on the lock-screen pill for a few seconds after the password was sent; it can't be tapped again meanwhile.
  ///
  /// In en, this message translates to:
  /// **'Unlocking…'**
  String get deskUnlocking;

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

  /// Error when a pairing code can’t be parsed.
  ///
  /// In en, this message translates to:
  /// **'The pairing code isn’t valid. Scan it again or copy it again.'**
  String get errorBadPairingCode;

  /// Error from an older Mac when opening a binary file.
  ///
  /// In en, this message translates to:
  /// **'This is a binary file and can’t be opened as text.'**
  String get errorBinaryFile;

  /// Error when the socket to the Mac can’t be opened.
  ///
  /// In en, this message translates to:
  /// **'Can’t connect to the Mac. Make sure Posse is open on the Mac and both are on the same network.'**
  String get errorCantConnect;

  /// Error when the connection to the Mac drops; the app reconnects by itself.
  ///
  /// In en, this message translates to:
  /// **'Lost the connection to the Mac. Reconnecting…'**
  String get errorConnectionLost;

  /// Error from an older Mac when a file is too large.
  ///
  /// In en, this message translates to:
  /// **'The file is too large to open on the phone.'**
  String get errorFileTooLarge;

  /// Error when the first connection after pairing keeps failing (Mac off, or network permission not granted).
  ///
  /// In en, this message translates to:
  /// **'Can’t connect to the Mac. Make sure Posse is open on the Mac and both are on the same network, and allow Posse to use wireless data and the local network in the phone’s Settings.'**
  String get errorFirstConnect;

  /// Error when creating a session and the Mac’s reply has no terminal.
  ///
  /// In en, this message translates to:
  /// **'The Mac didn’t return the new terminal'**
  String get errorNoNewTerminal;

  /// Error from an older Mac when the path isn’t a file.
  ///
  /// In en, this message translates to:
  /// **'This isn’t a file.'**
  String get errorNotAFile;

  /// Error when an action needs the Mac but the phone isn’t connected.
  ///
  /// In en, this message translates to:
  /// **'Not connected to the Mac yet'**
  String get errorNotConnected;

  /// Error when the Mac’s Posse rejects the phone’s handshake because it is an older version.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old to connect to this app. Update Posse on the Mac.'**
  String get errorDesktopTooOld;

  /// Error when the Mac’s Posse doesn’t know a request.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old for this. Update Posse on the Mac.'**
  String get errorOldDesktop;

  /// Error from an older Mac when a path is outside the workspace.
  ///
  /// In en, this message translates to:
  /// **'The path isn’t in the workspace.'**
  String get errorOutsideWorkspace;

  /// Error shown when a request to the Mac times out.
  ///
  /// In en, this message translates to:
  /// **'The Mac isn’t responding. It may be asleep, or the network is down.'**
  String get errorTimeout;

  /// File viewer: shown instead of the content of a binary file.
  ///
  /// In en, this message translates to:
  /// **'Binary file; its content can’t be shown on the phone'**
  String get filesBinary;

  /// Markdown preview: brief notice when a link can’t be opened. {url} is the link.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t open this link: {url}'**
  String filesCantOpenLink(String url);

  /// Chat and Terminal: tooltip of the top-bar button that opens Git changes and the file tree.
  ///
  /// In en, this message translates to:
  /// **'Changes and Files'**
  String get filesChangesAndFiles;

  /// Markdown preview: title of a collapsible <details> section that has no <summary>.
  ///
  /// In en, this message translates to:
  /// **'Details'**
  String get filesDetails;

  /// Download dialog and video player: error when the connection dropped mid-download.
  ///
  /// In en, this message translates to:
  /// **'Lost the connection to the Mac. The downloaded part is kept.'**
  String get filesDownloadDisconnected;

  /// Download dialog: error when the computer doesn’t support downloads.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old. Update it to download files.'**
  String get filesDownloadOldPosse;

  /// Download dialog: error when the download timed out.
  ///
  /// In en, this message translates to:
  /// **'The network is too slow and the download didn’t finish. The downloaded part is kept.'**
  String get filesDownloadSlow;

  /// Files: empty state of a folder.
  ///
  /// In en, this message translates to:
  /// **'This folder is empty'**
  String get filesEmptyFolder;

  /// Download dialog: progress text before the size is known.
  ///
  /// In en, this message translates to:
  /// **'Getting the file from the Mac…'**
  String get filesFetching;

  /// Video player: progress text before the size is known.
  ///
  /// In en, this message translates to:
  /// **'Getting the video from the Mac…'**
  String get filesFetchingVideo;

  /// Video player: progress text. {done} and {total} are formatted sizes like “12 MB”.
  ///
  /// In en, this message translates to:
  /// **'Getting the video from the Mac  {done} / {total}'**
  String filesFetchingVideoProgress(String done, String total);

  /// Files: placeholder of the search button above the file tree.
  ///
  /// In en, this message translates to:
  /// **'Find by name or path'**
  String get filesFindButton;

  /// Files: placeholder of the search field on the find page.
  ///
  /// In en, this message translates to:
  /// **'File name or path'**
  String get filesFindHint;

  /// Save: error when the photo library permission was denied.
  ///
  /// In en, this message translates to:
  /// **'Posse can’t access Photos. Allow access to Photos in Settings.'**
  String get filesNoAlbumAccess;

  /// File viewer: notice above a large file shown without syntax colors.
  ///
  /// In en, this message translates to:
  /// **'Large file; syntax highlighting is off'**
  String get filesNoHighlight;

  /// Save: error when the phone is out of space.
  ///
  /// In en, this message translates to:
  /// **'Not enough space on the phone; nothing was saved to Photos'**
  String get filesNoSpace;

  /// Save (Android): error when the storage permission was denied.
  ///
  /// In en, this message translates to:
  /// **'Posse can’t access storage. Allow storage access in Settings.'**
  String get filesNoStorageAccess;

  /// Markdown preview: brief notice when a relative link points outside the workspace.
  ///
  /// In en, this message translates to:
  /// **'This file isn’t in the current workspace and can’t be opened on the phone'**
  String get filesNotInWorkspace;

  /// Video player: tooltip of the pause button.
  ///
  /// In en, this message translates to:
  /// **'Pause'**
  String get filesPause;

  /// Video player: tooltip of the play button.
  ///
  /// In en, this message translates to:
  /// **'Play'**
  String get filesPlay;

  /// File viewer: button that switches a Markdown file from source to rendered preview.
  ///
  /// In en, this message translates to:
  /// **'Preview'**
  String get filesPreview;

  /// Download dialog and video player: button that continues an interrupted download.
  ///
  /// In en, this message translates to:
  /// **'Resume Download'**
  String get filesResumeDownload;

  /// Save sheet and viewer: action that saves an image or video to the photo library.
  ///
  /// In en, this message translates to:
  /// **'Save to Photos'**
  String get filesSaveToAlbum;

  /// Save: error when saving to the photo library failed. {error} is the system’s message.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t save to Photos: {error}'**
  String filesSaveToAlbumFailed(String error);

  /// Save sheet: subtitle of Save to Photos.
  ///
  /// In en, this message translates to:
  /// **'Download to the phone and put it in the “Posse” album in Photos'**
  String get filesSaveToAlbumNote;

  /// Save sheet and viewer (Android): action that saves a file to Downloads/Posse.
  ///
  /// In en, this message translates to:
  /// **'Save to Downloads'**
  String get filesSaveToDownloads;

  /// Save (Android): error when saving to Downloads failed. {error} is the system’s message.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t save to Downloads: {error}'**
  String filesSaveToDownloadsFailed(String error);

  /// Save sheet (Android): subtitle of Save to Downloads.
  ///
  /// In en, this message translates to:
  /// **'Download to the phone and put it in Downloads/Posse, where file managers can find it'**
  String get filesSaveToDownloadsNote;

  /// Save: brief notice after saving to the photo library.
  ///
  /// In en, this message translates to:
  /// **'Saved to the “Posse” album in Photos'**
  String get filesSavedToAlbum;

  /// Save (Android): brief notice after saving to Downloads. {name} is the saved file name.
  ///
  /// In en, this message translates to:
  /// **'Saved to Downloads/Posse: {name}'**
  String filesSavedToDownloads(String name);

  /// Save sheet and viewer: action that opens the system share sheet.
  ///
  /// In en, this message translates to:
  /// **'Share'**
  String get filesShare;

  /// Save sheet: subtitle of Share when it is the main action.
  ///
  /// In en, this message translates to:
  /// **'Download to the phone, then save it to Files or send it to another app with the share sheet'**
  String get filesShareNote;

  /// Save sheet: subtitle of Share as the second action.
  ///
  /// In en, this message translates to:
  /// **'Download to the phone and send it to another app'**
  String get filesShareOtherNote;

  /// File viewer: button that switches a Markdown file from preview to source.
  ///
  /// In en, this message translates to:
  /// **'Source'**
  String get filesSource;

  /// Files: page title; the workspace path is shown below it.
  ///
  /// In en, this message translates to:
  /// **'Files'**
  String get filesTitle;

  /// Download dialog: error for files over 500 MB.
  ///
  /// In en, this message translates to:
  /// **'The file is larger than 500 MB and won’t be downloaded to the phone'**
  String get filesTooLarge;

  /// File viewer: notice above a truncated file.
  ///
  /// In en, this message translates to:
  /// **'The file is too large; showing only the first 512 KB'**
  String get filesTruncated;

  /// Video player: error when the player failed. {error} is the reason.
  ///
  /// In en, this message translates to:
  /// **'This video can’t be played on the phone: {error}'**
  String filesVideoCantPlay(String error);

  /// Video player: error when the computer doesn’t support video download.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old. Update it to play videos on your phone.'**
  String get filesVideoOldPosse;

  /// Video player: error when the download timed out.
  ///
  /// In en, this message translates to:
  /// **'The network is too slow and the video didn’t finish downloading. The downloaded part is kept.'**
  String get filesVideoSlow;

  /// Video player: error for videos over 500 MB.
  ///
  /// In en, this message translates to:
  /// **'The video is larger than 500 MB and won’t be downloaded to the phone'**
  String get filesVideoTooLarge;

  /// Git changes: branch card chip, commits not yet pushed.
  ///
  /// In en, this message translates to:
  /// **'{count} ahead'**
  String gitAhead(int count);

  /// Git changes: branch card chip, commits not yet pulled.
  ///
  /// In en, this message translates to:
  /// **'{count} behind'**
  String gitBehind(int count);

  /// Git changes: title of the bottom sheet listing branches to switch to.
  ///
  /// In en, this message translates to:
  /// **'Switch Branch'**
  String get gitBranchesTitle;

  /// Git changes: title of the sheet choosing which repository to switch branches in, when the workspace has several.
  ///
  /// In en, this message translates to:
  /// **'Switch Branch In'**
  String get gitBranchesPickRepo;

  /// Git changes: placeholder of the search box in the branch sheet.
  ///
  /// In en, this message translates to:
  /// **'Search branches'**
  String get gitBranchesSearch;

  /// Git changes: branch sheet section of local branches.
  ///
  /// In en, this message translates to:
  /// **'Local'**
  String get gitBranchesLocal;

  /// Git changes: branch sheet section of remote branches.
  ///
  /// In en, this message translates to:
  /// **'Remote'**
  String get gitBranchesRemote;

  /// Git changes: branch sheet text when the search matches nothing.
  ///
  /// In en, this message translates to:
  /// **'No matching branches'**
  String get gitBranchesNoMatch;

  /// Git changes: branch sheet row subtitle of the checked-out branch.
  ///
  /// In en, this message translates to:
  /// **'Current branch'**
  String get gitBranchCurrent;

  /// Git changes: branch sheet row subtitle when another Posse workspace has the branch checked out; tapping opens that workspace.
  ///
  /// In en, this message translates to:
  /// **'Checked out in {workspace}'**
  String gitBranchInWorkspace(String workspace);

  /// Git changes: branch sheet row subtitle when a worktree outside Posse has the branch checked out; it can't be switched to. dir is the folder name.
  ///
  /// In en, this message translates to:
  /// **'Checked out in worktree {dir}'**
  String gitBranchInWorktree(String dir);

  /// Git changes: branch sheet notice while a merge or rebase is in progress. operation is a git word like rebase.
  ///
  /// In en, this message translates to:
  /// **'Finish the {operation} before switching branches'**
  String gitBranchesBusy(String operation);

  /// Git changes: title of the confirmation before switching branches while an agent is working.
  ///
  /// In en, this message translates to:
  /// **'{agent} Is Working in This Workspace'**
  String gitCheckoutAgentTitle(String agent);

  /// Git changes: body of the confirmation before switching branches while an agent is working.
  ///
  /// In en, this message translates to:
  /// **'Switching branches changes files in the working directory and may disrupt the edits it is making.'**
  String get gitCheckoutAgentBody;

  /// Git changes: confirm button to switch branches while an agent is working.
  ///
  /// In en, this message translates to:
  /// **'Switch Anyway'**
  String get gitCheckoutAnyway;

  /// Git changes: snack bar after switching branches.
  ///
  /// In en, this message translates to:
  /// **'Switched to {branch}'**
  String gitSwitchedTo(String branch);

  /// Git diff: shown for a binary file.
  ///
  /// In en, this message translates to:
  /// **'Binary file; the diff can’t be shown on the phone'**
  String get gitBinaryDiff;

  /// Git changes: header above the file list.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} file changed} other {{count} files changed}}'**
  String gitChangedFiles(int count);

  /// Git changes: group header for modified, added, deleted and renamed files.
  ///
  /// In en, this message translates to:
  /// **'Changes'**
  String get gitChanges;

  /// Git changes: empty state.
  ///
  /// In en, this message translates to:
  /// **'The workspace is clean, no changes'**
  String get gitClean;

  /// Git changes: group header for conflicted files; also shown instead of line counts for a conflicted file.
  ///
  /// In en, this message translates to:
  /// **'Conflicts'**
  String get gitConflicts;

  /// Git image diff: placeholder on the Now side for a deleted image.
  ///
  /// In en, this message translates to:
  /// **'Deleted'**
  String get gitDeleted;

  /// Git changes: shown in place of the branch name when HEAD is detached.
  ///
  /// In en, this message translates to:
  /// **'Detached HEAD'**
  String get gitDetachedHead;

  /// Git diff: notice above a truncated diff.
  ///
  /// In en, this message translates to:
  /// **'The diff is too long; showing only the beginning'**
  String get gitDiffTruncated;

  /// Git changes: toggle option that lists files without directories.
  ///
  /// In en, this message translates to:
  /// **'Flat'**
  String get gitFlatView;

  /// Git diff: start of a hunk header, the first line number of the change.
  ///
  /// In en, this message translates to:
  /// **'Line {line}'**
  String gitHunkLine(int line);

  /// Git image diff: label above the old image.
  ///
  /// In en, this message translates to:
  /// **'Before'**
  String get gitImageBefore;

  /// Git image diff: placeholder on the Before side for a new image.
  ///
  /// In en, this message translates to:
  /// **'New file; it didn’t exist before'**
  String get gitImageNewFile;

  /// Git image diff: label above the current image.
  ///
  /// In en, this message translates to:
  /// **'Now'**
  String get gitImageNow;

  /// Git image diff: placeholder for images over 10 MB.
  ///
  /// In en, this message translates to:
  /// **'The image is larger than 10 MB and isn’t shown on the phone'**
  String get gitImageTooLarge;

  /// Git image diff: placeholder when the image can’t be decoded.
  ///
  /// In en, this message translates to:
  /// **'This image can’t be drawn on the phone'**
  String get gitImageUndrawable;

  /// Git diff: empty state.
  ///
  /// In en, this message translates to:
  /// **'No differences'**
  String get gitNoDifferences;

  /// Git changes: shown instead of line counts for a binary file.
  ///
  /// In en, this message translates to:
  /// **'Binary'**
  String get gitStatsBinary;

  /// Git changes: shown instead of line counts for an image file.
  ///
  /// In en, this message translates to:
  /// **'Image'**
  String get gitStatsImage;

  /// Git changes: shown instead of line counts for a new empty file.
  ///
  /// In en, this message translates to:
  /// **'New File'**
  String get gitStatsNewFile;

  /// Git changes: branch card line naming the upstream branch.
  ///
  /// In en, this message translates to:
  /// **'Tracking {upstream}'**
  String gitTracking(String upstream);

  /// Git changes: toggle option that groups files by directory.
  ///
  /// In en, this message translates to:
  /// **'By Directory'**
  String get gitTreeView;

  /// Git changes: group header for untracked files.
  ///
  /// In en, this message translates to:
  /// **'Unversioned Files'**
  String get gitUnversioned;

  /// Git diff: tooltip of the top-bar button that opens the whole file.
  ///
  /// In en, this message translates to:
  /// **'View Full File'**
  String get gitViewFullFile;

  /// History: brief notice after Continue on Computer when the session was already open.
  ///
  /// In en, this message translates to:
  /// **'This session is already open on the Mac'**
  String get historyAlreadyOpen;

  /// History: notice above search results when no session matches every word.
  ///
  /// In en, this message translates to:
  /// **'No session contains all the words. Showing sessions with any of them.'**
  String get historyAnyWordNotice;

  /// History: tooltip of the grouping button while grouped by folder.
  ///
  /// In en, this message translates to:
  /// **'Grouped by folder. Tap to group by time.'**
  String get historyByFolderTip;

  /// History: tooltip of the grouping button while grouped by time.
  ///
  /// In en, this message translates to:
  /// **'Grouped by time. Tap to group by folder.'**
  String get historyByTimeTip;

  /// History: tooltip of the button that clears the search field.
  ///
  /// In en, this message translates to:
  /// **'Clear'**
  String get historyClearSearch;

  /// History: button at the bottom of a session that resumes it on the computer.
  ///
  /// In en, this message translates to:
  /// **'Continue on Mac'**
  String get historyContinueOnComputer;

  /// History: empty state of all sessions on the computer.
  ///
  /// In en, this message translates to:
  /// **'No session history on the Mac yet'**
  String get historyEmptyComputer;

  /// History: empty state when filtered by an agent. {agent} is the agent name.
  ///
  /// In en, this message translates to:
  /// **'No {agent} session history on the Mac yet'**
  String historyEmptyComputerAgent(String agent);

  /// History: empty state in a project’s history.
  ///
  /// In en, this message translates to:
  /// **'No session history in this folder yet'**
  String get historyEmptyFolder;

  /// History: empty state in a project’s history when filtered by an agent. {agent} is the agent name.
  ///
  /// In en, this message translates to:
  /// **'No {agent} session history in this folder yet'**
  String historyEmptyFolderAgent(String agent);

  /// History: group header for sessions updated in the last 30 days.
  ///
  /// In en, this message translates to:
  /// **'Previous 30 Days'**
  String get historyLast30Days;

  /// History: group header for sessions updated in the last 7 days.
  ///
  /// In en, this message translates to:
  /// **'Previous 7 Days'**
  String get historyLast7Days;

  /// History: empty state of a search.
  ///
  /// In en, this message translates to:
  /// **'No results'**
  String get historyNoResults;

  /// History: error when the resumed session didn’t show up in time.
  ///
  /// In en, this message translates to:
  /// **'The session hasn’t appeared on the Mac yet. Look for it on Home in a moment.'**
  String get historyNotYetOpen;

  /// History: error when the computer doesn’t support history.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac is too old. Update it to see History.'**
  String get historyOldPosse;

  /// History: group header for sessions last updated more than 30 days ago.
  ///
  /// In en, this message translates to:
  /// **'Older'**
  String get historyOlder;

  /// History: button label while the session is being resumed on the computer.
  ///
  /// In en, this message translates to:
  /// **'Opening…'**
  String get historyOpening;

  /// History: brief notice after Continue on Computer resumed the session.
  ///
  /// In en, this message translates to:
  /// **'Continued this session on the Mac'**
  String get historyResumed;

  /// History: placeholder of the search field.
  ///
  /// In en, this message translates to:
  /// **'Search sessions'**
  String get historySearchHint;

  /// History: group header for sessions updated today.
  ///
  /// In en, this message translates to:
  /// **'Today'**
  String get historyToday;

  /// History: group header (By Folder) for sessions whose folder is unknown.
  ///
  /// In en, this message translates to:
  /// **'Unknown Folder'**
  String get historyUnknownFolder;

  /// History: title of a session that has no title.
  ///
  /// In en, this message translates to:
  /// **'(Untitled)'**
  String get historyUntitled;

  /// History: action in the notice after resuming, opens the session on the phone.
  ///
  /// In en, this message translates to:
  /// **'View on Phone'**
  String get historyViewOnPhone;

  /// History: group header for sessions updated yesterday.
  ///
  /// In en, this message translates to:
  /// **'Yesterday'**
  String get historyYesterday;

  /// Address shown for a hole-punched direct QUIC connection.
  ///
  /// In en, this message translates to:
  /// **'Direct · QUIC'**
  String get homeAddressDirect;

  /// Address shown for a connection through the relay (the relay’s address is hidden).
  ///
  /// In en, this message translates to:
  /// **'Via Relay · QUIC'**
  String get homeAddressRelay;

  /// Pairing page note after scanning from the home screen failed without a specific reason.
  ///
  /// In en, this message translates to:
  /// **'Can’t connect to this Mac.'**
  String get homeCantConnect;

  /// Name used for a paired Mac when it has no name yet.
  ///
  /// In en, this message translates to:
  /// **'Mac'**
  String get homeComputerFallback;

  /// Home: Mac pill while connecting.
  ///
  /// In en, this message translates to:
  /// **'Connecting to the Mac…'**
  String get homeConnecting;

  /// Mac list: button that disconnects from the current Mac.
  ///
  /// In en, this message translates to:
  /// **'Disconnect'**
  String get homeDisconnect;

  /// Home: Mac pill after the user disconnected, when no Mac is chosen.
  ///
  /// In en, this message translates to:
  /// **'Disconnected'**
  String get homeDisconnected;

  /// Disconnected home: title of the list of paired Macs.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {Disconnected · {count} Mac available} other {Disconnected · {count} Macs available}}'**
  String homeDisconnectedList(int count);

  /// Home: Mac pill after the user disconnected. name is the Mac’s name.
  ///
  /// In en, this message translates to:
  /// **'{name} · Disconnected'**
  String homeDisconnectedNamed(String name);

  /// Home: Mac pill when connecting failed, when no Mac is chosen.
  ///
  /// In en, this message translates to:
  /// **'Can’t Connect'**
  String get homeFailed;

  /// Home: Mac pill when connecting failed. name is the Mac’s name.
  ///
  /// In en, this message translates to:
  /// **'{name} · Can’t Connect'**
  String homeFailedNamed(String name);

  /// Button in the dialog confirming forgetting a Mac.
  ///
  /// In en, this message translates to:
  /// **'Forget'**
  String get homeForget;

  /// Body of the dialog confirming forgetting a Mac.
  ///
  /// In en, this message translates to:
  /// **'You’ll need to scan the code again to connect to it.'**
  String get homeForgetBody;

  /// Mac actions sheet: remove this pairing.
  ///
  /// In en, this message translates to:
  /// **'Forget This Mac'**
  String get homeForgetComputer;

  /// Title of the dialog confirming forgetting a Mac. name is the Mac’s name.
  ///
  /// In en, this message translates to:
  /// **'Forget {name}?'**
  String homeForgetConfirm(String name);

  /// Home top bar: tooltip of the History button.
  ///
  /// In en, this message translates to:
  /// **'History'**
  String get homeHistoryTip;

  /// Mac list: hint under the buttons.
  ///
  /// In en, this message translates to:
  /// **'Long-press a Mac to rename or forget it'**
  String get homeLongPressHint;

  /// Home › Running: empty state.
  ///
  /// In en, this message translates to:
  /// **'No running sessions'**
  String get homeNoRunning;

  /// Home: empty state when the Mac has no workspaces.
  ///
  /// In en, this message translates to:
  /// **'Posse on the Mac has no workspaces yet'**
  String get homeNoWorkspaces;

  /// Home: Mac pill while reconnecting.
  ///
  /// In en, this message translates to:
  /// **'Connection lost, reconnecting…'**
  String get homeReconnecting;

  /// Mac list row and Settings section/toggle: view and control the Mac’s screen.
  ///
  /// In en, this message translates to:
  /// **'Remote Desktop'**
  String get homeRemoteDesktop;

  /// Mac actions sheet: rename this Mac.
  ///
  /// In en, this message translates to:
  /// **'Rename'**
  String get homeRename;

  /// Rename dialog: helper text under the field.
  ///
  /// In en, this message translates to:
  /// **'Only changes the name shown on this phone. Leave empty to use the Mac’s own name.'**
  String get homeRenameHelper;

  /// Rename dialog: placeholder when the Mac has no name of its own.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get homeRenameHint;

  /// Title of the rename dialog.
  ///
  /// In en, this message translates to:
  /// **'Rename Mac'**
  String get homeRenameTitle;

  /// Mac list and disconnected home: button that scans a code to pair another Mac.
  ///
  /// In en, this message translates to:
  /// **'Scan to Add a Mac'**
  String get homeScanToAdd;

  /// Home top bar: tooltip of the Settings button.
  ///
  /// In en, this message translates to:
  /// **'Settings'**
  String get homeSettingsTip;

  /// Home: segment that lists sessions by project.
  ///
  /// In en, this message translates to:
  /// **'Projects'**
  String get homeTabProjects;

  /// Home: segment that lists all running agent sessions by status.
  ///
  /// In en, this message translates to:
  /// **'Running'**
  String get homeTabRunning;

  /// Home: banner counting sessions waiting for approval or an answer.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} session needs you} other {{count} sessions need you}}'**
  String homeWaitingBanner(int count);

  /// Home: session row detail while disconnected from the Mac.
  ///
  /// In en, this message translates to:
  /// **'Waiting to connect'**
  String get homeWaitingConnection;

  /// Key editor: placeholder of the Alt key field.
  ///
  /// In en, this message translates to:
  /// **'e.g. b, f, .'**
  String get keysAltHint;

  /// Key editor: label of the Alt key field.
  ///
  /// In en, this message translates to:
  /// **'Alt plus which key'**
  String get keysAltWhich;

  /// Key bar settings: section title of the ordered list.
  ///
  /// In en, this message translates to:
  /// **'Key Bar'**
  String get keysBar;

  /// Key bar settings: note under the list.
  ///
  /// In en, this message translates to:
  /// **'The keyboard toggle on the far left stays put; the rest go left to right in the order listed here. Tap a key you added to edit or delete it.'**
  String get keysBarNote;

  /// Key bar settings: subtitle of a built-in key.
  ///
  /// In en, this message translates to:
  /// **'Built-in'**
  String get keysBuiltin;

  /// Key bar settings: built-in Clear key.
  ///
  /// In en, this message translates to:
  /// **'Clear Input'**
  String get keysClear;

  /// Key bar settings: built-in Ctrl+C key.
  ///
  /// In en, this message translates to:
  /// **'Ctrl + C: stop the program'**
  String get keysCtrlC;

  /// Key editor: placeholder of the Ctrl key field.
  ///
  /// In en, this message translates to:
  /// **'e.g. r, d, z, l'**
  String get keysCtrlHint;

  /// Key editor: error when the Ctrl key has no control code.
  ///
  /// In en, this message translates to:
  /// **'Ctrl works only with a letter or @ [ \\ ] ^ _ ?'**
  String get keysCtrlInvalid;

  /// Key editor: label of the Ctrl key field.
  ///
  /// In en, this message translates to:
  /// **'Ctrl plus which key'**
  String get keysCtrlWhich;

  /// Key bar settings: built-in arrow key.
  ///
  /// In en, this message translates to:
  /// **'Arrow key: down'**
  String get keysDown;

  /// Key editor title for an existing key.
  ///
  /// In en, this message translates to:
  /// **'Edit Shortcut'**
  String get keysEdit;

  /// Key bar settings: built-in Enter key.
  ///
  /// In en, this message translates to:
  /// **'Return'**
  String get keysEnter;

  /// Key editor: switch for sending Return after the text.
  ///
  /// In en, this message translates to:
  /// **'Press Return After Typing'**
  String get keysEnterAfter;

  /// Key bar settings: built-in Esc key.
  ///
  /// In en, this message translates to:
  /// **'Esc: interrupt, exit'**
  String get keysEsc;

  /// Key bar settings: built-in Exit key.
  ///
  /// In en, this message translates to:
  /// **'Exit Agent (/exit)'**
  String get keysExit;

  /// Key bar settings: built-in Send File key.
  ///
  /// In en, this message translates to:
  /// **'Send File (photos, videos, files)'**
  String get keysImage;

  /// Key editor: segment for a special key such as PgUp.
  ///
  /// In en, this message translates to:
  /// **'Special'**
  String get keysKindSpecial;

  /// Key editor: segment for a text key.
  ///
  /// In en, this message translates to:
  /// **'Text'**
  String get keysKindText;

  /// Key editor: label of the key cap text field.
  ///
  /// In en, this message translates to:
  /// **'Button Label'**
  String get keysLabel;

  /// Key editor: placeholder of the key cap field. shown is the generated label.
  ///
  /// In en, this message translates to:
  /// **'Leave empty to use {shown}'**
  String keysLabelHint(String shown);

  /// Key editor: placeholder of the key cap field before anything to send is entered.
  ///
  /// In en, this message translates to:
  /// **'Leave empty to use the text'**
  String get keysLabelHintEmpty;

  /// Key bar settings: built-in arrow key.
  ///
  /// In en, this message translates to:
  /// **'Arrow key: left'**
  String get keysLeft;

  /// Key bar settings: add button, tooltip, and the editor title for a new key.
  ///
  /// In en, this message translates to:
  /// **'New Shortcut'**
  String get keysNew;

  /// Key bar settings: subtitle of New Shortcut.
  ///
  /// In en, this message translates to:
  /// **'Frequently used text, a Ctrl combination, or a special key like PgUp'**
  String get keysNewNote;

  /// Key bar settings: built-in Paste key.
  ///
  /// In en, this message translates to:
  /// **'Paste Clipboard'**
  String get keysPaste;

  /// Key bar settings: row that restores the default order.
  ///
  /// In en, this message translates to:
  /// **'Restore Default Order'**
  String get keysResetOrder;

  /// Key bar settings: subtitle of Restore Default Order.
  ///
  /// In en, this message translates to:
  /// **'Keys you added are kept and placed after Send File and Paste'**
  String get keysResetOrderNote;

  /// Key bar settings: built-in arrow key.
  ///
  /// In en, this message translates to:
  /// **'Arrow key: right'**
  String get keysRight;

  /// Key bar settings: built-in Send Now key.
  ///
  /// In en, this message translates to:
  /// **'Ctrl + Enter: send queued messages to Claude now'**
  String get keysSendNow;

  /// Key bar settings: built-in Shift+Tab key.
  ///
  /// In en, this message translates to:
  /// **'Shift + Tab: switch mode'**
  String get keysShiftTab;

  /// Key editor: label of the special key choices.
  ///
  /// In en, this message translates to:
  /// **'Which Key'**
  String get keysSpecialWhich;

  /// Key bar settings: built-in Tab key.
  ///
  /// In en, this message translates to:
  /// **'Tab: complete'**
  String get keysTab;

  /// Key editor: label of the text field.
  ///
  /// In en, this message translates to:
  /// **'Text to Type'**
  String get keysText;

  /// Key editor: placeholder of the text field.
  ///
  /// In en, this message translates to:
  /// **'e.g. /compact'**
  String get keysTextHint;

  /// Key bar settings page title.
  ///
  /// In en, this message translates to:
  /// **'Terminal Shortcuts'**
  String get keysTitle;

  /// Key bar settings: what a custom text key does. text is the text.
  ///
  /// In en, this message translates to:
  /// **'Type “{text}”'**
  String keysTypeText(String text);

  /// Key bar settings: what a custom text key with Return does. text is the text.
  ///
  /// In en, this message translates to:
  /// **'Type “{text}” and press Return'**
  String keysTypeTextEnter(String text);

  /// Key bar settings: built-in arrow key.
  ///
  /// In en, this message translates to:
  /// **'Arrow key: up'**
  String get keysUp;

  /// Mermaid diagram card: error above the source. error is the reason.
  ///
  /// In en, this message translates to:
  /// **'Can’t draw the diagram: {error}'**
  String mermaidCantDraw(String error);

  /// Mermaid error: the diagram has no size.
  ///
  /// In en, this message translates to:
  /// **'The diagram has no size'**
  String get mermaidNoSize;

  /// Mermaid error: the renderer produced no SVG.
  ///
  /// In en, this message translates to:
  /// **'Pele didn’t draw an SVG'**
  String get mermaidNoSvg;

  /// Mermaid error: rendering timed out.
  ///
  /// In en, this message translates to:
  /// **'Drawing the diagram timed out'**
  String get mermaidTimeout;

  /// Pairing page: description when adding another Mac.
  ///
  /// In en, this message translates to:
  /// **'You can pair several Macs and connect to one at a time. Switch between them at the top of the home screen.'**
  String get pairAddNote;

  /// Pairing page title when adding another Mac.
  ///
  /// In en, this message translates to:
  /// **'Add a Mac'**
  String get pairAddTitle;

  /// Pairing page: note when camera permission is denied.
  ///
  /// In en, this message translates to:
  /// **'Posse needs camera access to scan. Allow it in Settings, or paste the pairing code.'**
  String get pairCameraDenied;

  /// Pairing page: note when the camera can’t start.
  ///
  /// In en, this message translates to:
  /// **'Can’t open the camera. You can paste the pairing code instead.'**
  String get pairCameraFailed;

  /// Pairing page: note after tapping Paste Pairing Code with an empty clipboard.
  ///
  /// In en, this message translates to:
  /// **'The clipboard is empty.'**
  String get pairClipboardEmpty;

  /// Pairing page: note after tapping Paste Pairing Code when the clipboard isn’t a code.
  ///
  /// In en, this message translates to:
  /// **'The clipboard doesn’t contain a pairing code.'**
  String get pairClipboardNotCode;

  /// Pairing page: button that connects with the typed code.
  ///
  /// In en, this message translates to:
  /// **'Connect'**
  String get pairConnect;

  /// Pairing page title on first launch.
  ///
  /// In en, this message translates to:
  /// **'Connect to Posse'**
  String get pairConnectTitle;

  /// Snack bar after connecting. name is the Mac’s name.
  ///
  /// In en, this message translates to:
  /// **'Connected to {name}'**
  String pairConnected(String name);

  /// Snack bar after connecting. name is the Mac’s name, address is host:port or “Via Relay · QUIC”.
  ///
  /// In en, this message translates to:
  /// **'Connected to {name} at {address}'**
  String pairConnectedAt(String name, String address);

  /// Pairing page: button label while connecting.
  ///
  /// In en, this message translates to:
  /// **'Connecting…'**
  String get pairConnecting;

  /// Pairing page: button that hides the manual entry field.
  ///
  /// In en, this message translates to:
  /// **'Hide'**
  String get pairHideManual;

  /// Pairing page: description on first launch.
  ///
  /// In en, this message translates to:
  /// **'Follow your agents’ progress, reply, and approve requests from your phone.'**
  String get pairIntro;

  /// Button on the pairing page and the camera scanner: type the pairing code.
  ///
  /// In en, this message translates to:
  /// **'Enter Manually'**
  String get pairManual;

  /// Button on the pairing page and the camera scanner.
  ///
  /// In en, this message translates to:
  /// **'Paste Pairing Code'**
  String get pairPasteCode;

  /// Camera scanner: hint above the frame.
  ///
  /// In en, this message translates to:
  /// **'Point at the pairing code in Posse on your Mac'**
  String get pairScanAim;

  /// Pairing page: button that opens the camera.
  ///
  /// In en, this message translates to:
  /// **'Scan to Pair'**
  String get pairScanButton;

  /// Camera scanner: note under the frame after tapping Paste Pairing Code with an empty clipboard.
  ///
  /// In en, this message translates to:
  /// **'The clipboard is empty'**
  String get pairScanClipboardEmpty;

  /// Camera scanner: note under the frame when the clipboard isn’t a code.
  ///
  /// In en, this message translates to:
  /// **'The clipboard doesn’t contain a pairing code'**
  String get pairScanClipboardNotCode;

  /// Pairing page: detail of step 1.
  ///
  /// In en, this message translates to:
  /// **'Click the phone icon at the bottom left of the window'**
  String get pairStepOpenDetail;

  /// Pairing page: title of step 1.
  ///
  /// In en, this message translates to:
  /// **'Open Posse on your Mac'**
  String get pairStepOpenTitle;

  /// Pairing page: detail of step 2.
  ///
  /// In en, this message translates to:
  /// **'Or copy the pairing code on the Mac and paste it here'**
  String get pairStepScanDetail;

  /// Pairing page: title of step 2.
  ///
  /// In en, this message translates to:
  /// **'Scan the QR code'**
  String get pairStepScanTitle;

  /// Session More sheet: closes an agent session.
  ///
  /// In en, this message translates to:
  /// **'Close Session'**
  String get sessionCloseSession;

  /// Body of the dialog confirming closing an agent session. agent is the agent’s name, e.g. Claude.
  ///
  /// In en, this message translates to:
  /// **'{agent} will be stopped and unfinished work interrupted. The conversation stays in the Mac’s session history and can be resumed on the Mac later.'**
  String sessionCloseSessionBody(String agent);

  /// Title of the dialog confirming closing an agent session.
  ///
  /// In en, this message translates to:
  /// **'Close This Session?'**
  String get sessionCloseSessionConfirm;

  /// Session More sheet: closes a plain terminal.
  ///
  /// In en, this message translates to:
  /// **'Close Terminal'**
  String get sessionCloseTerminal;

  /// Body of the dialog confirming closing a terminal. title is the terminal’s title.
  ///
  /// In en, this message translates to:
  /// **'Programs running in “{title}” will be stopped.'**
  String sessionCloseTerminalBody(String title);

  /// Title of the dialog confirming closing a terminal.
  ///
  /// In en, this message translates to:
  /// **'Close This Terminal?'**
  String get sessionCloseTerminalConfirm;

  /// Session More sheet: switch that keeps the terminal at the Mac’s size.
  ///
  /// In en, this message translates to:
  /// **'Desktop Layout'**
  String get sessionDeskLayout;

  /// Session More sheet: subtitle of Desktop Layout.
  ///
  /// In en, this message translates to:
  /// **'Shrinks to the screen width; pinch to zoom, then drag to scroll'**
  String get sessionDeskLayoutNote;

  /// Home session row: the agent waits for approval of a tool. tool is the tool name.
  ///
  /// In en, this message translates to:
  /// **'Waiting for you to approve {tool}'**
  String sessionDetailApprove(String tool);

  /// Home session row: the agent finished a turn.
  ///
  /// In en, this message translates to:
  /// **'Turn finished'**
  String get sessionDetailDone;

  /// Home session row: the terminal wasn’t restored after the Mac restarted; opening it starts it.
  ///
  /// In en, this message translates to:
  /// **'Not Restored: starts when you open it'**
  String get sessionDetailLost;

  /// Home session row: an idle agent without a saved chat, only the terminal can be viewed.
  ///
  /// In en, this message translates to:
  /// **'Terminal only'**
  String get sessionDetailTerminalOnly;

  /// Home session row: the agent is running a tool. tool is the tool name.
  ///
  /// In en, this message translates to:
  /// **'Using {tool}'**
  String sessionDetailUsing(String tool);

  /// Home session row: the agent is waiting for the user.
  ///
  /// In en, this message translates to:
  /// **'Waiting for you'**
  String get sessionDetailWaiting;

  /// Session More sheet: row that opens the session’s images and videos.
  ///
  /// In en, this message translates to:
  /// **'Images & Videos'**
  String get sessionMedia;

  /// Session More sheet: subtitle of Images & Videos.
  ///
  /// In en, this message translates to:
  /// **'Screenshots the agent took, generated images and videos, and pasted images'**
  String get sessionMediaNote;

  /// New Session sheet title and the home tooltip of its button.
  ///
  /// In en, this message translates to:
  /// **'New Session'**
  String get sessionNew;

  /// New Session: workspace row when the Mac has none.
  ///
  /// In en, this message translates to:
  /// **'No workspaces yet'**
  String get sessionNoWorkspaces;

  /// New Session: button when Terminal is chosen.
  ///
  /// In en, this message translates to:
  /// **'Open Terminal'**
  String get sessionOpenTerminal;

  /// New Session: label above the agent choices.
  ///
  /// In en, this message translates to:
  /// **'Open With'**
  String get sessionOpenWith;

  /// New Session: title of the workspace picker sheet.
  ///
  /// In en, this message translates to:
  /// **'Which Workspace'**
  String get sessionPickWorkspace;

  /// New Session: placeholder of the first message field.
  ///
  /// In en, this message translates to:
  /// **'First message (optional; you can say it later)'**
  String get sessionPromptHint;

  /// New Session: button when an agent is chosen. agent is the agent’s name.
  ///
  /// In en, this message translates to:
  /// **'Start Chat with {agent}'**
  String sessionStartChat(String agent);

  /// New Session: button label while starting.
  ///
  /// In en, this message translates to:
  /// **'Starting…'**
  String get sessionStarting;

  /// Workspace status: the agent finished.
  ///
  /// In en, this message translates to:
  /// **'Done'**
  String get sessionStateDone;

  /// Session status: the agent is idle.
  ///
  /// In en, this message translates to:
  /// **'Idle'**
  String get sessionStateIdle;

  /// Session status: the agent is starting.
  ///
  /// In en, this message translates to:
  /// **'Starting'**
  String get sessionStateStarting;

  /// Session status: waiting for the agents it dispatched.
  ///
  /// In en, this message translates to:
  /// **'Waiting on Tasks'**
  String get sessionStateTasks;

  /// Session status: finished a turn the user hasn’t looked at.
  ///
  /// In en, this message translates to:
  /// **'Turn finished, not seen yet'**
  String get sessionStateUnread;

  /// Session status: the agent is waiting for approval or an answer.
  ///
  /// In en, this message translates to:
  /// **'Needs You'**
  String get sessionStateWaiting;

  /// Session status: the agent is working.
  ///
  /// In en, this message translates to:
  /// **'Working'**
  String get sessionStateWorking;

  /// A plain terminal (no agent): session row detail and the choice in New Session.
  ///
  /// In en, this message translates to:
  /// **'Terminal'**
  String get sessionTerminal;

  /// Session More sheet: row that opens the skills and tools the session used.
  ///
  /// In en, this message translates to:
  /// **'Skills & Tools'**
  String get sessionTools;

  /// Session More sheet: subtitle of Skills & Tools.
  ///
  /// In en, this message translates to:
  /// **'Skills, MCP servers, and command-line tools used'**
  String get sessionToolsNote;

  /// New Session: title of the workspace section.
  ///
  /// In en, this message translates to:
  /// **'Workspace'**
  String get sessionWorkspace;

  /// Settings: section title for the app version and updates.
  ///
  /// In en, this message translates to:
  /// **'About'**
  String get settingsAbout;

  /// Settings: section title for the theme.
  ///
  /// In en, this message translates to:
  /// **'Appearance'**
  String get settingsAppearance;

  /// Settings: section title for the chat view.
  ///
  /// In en, this message translates to:
  /// **'Chat'**
  String get settingsChat;

  /// Settings › About: Check for Updates failed.
  ///
  /// In en, this message translates to:
  /// **'Check failed. Try again later.'**
  String get settingsCheckFailed;

  /// Settings › About: row that checks for a new version.
  ///
  /// In en, this message translates to:
  /// **'Check for Updates'**
  String get settingsCheckUpdate;

  /// Settings: section title for the paired Macs.
  ///
  /// In en, this message translates to:
  /// **'Mac'**
  String get settingsComputer;

  /// Settings › Remote Desktop: control mode where taps land where you touch.
  ///
  /// In en, this message translates to:
  /// **'Direct Touch'**
  String get settingsDeskDirect;

  /// Settings › Remote Desktop: note for Direct Touch.
  ///
  /// In en, this message translates to:
  /// **'Tap where you want to click, long-press then move to drag, and drag one finger to pan a zoomed-in view.'**
  String get settingsDeskDirectNote;

  /// Settings › Remote Desktop: note under the switch.
  ///
  /// In en, this message translates to:
  /// **'Tap the Mac’s name on the home screen and choose Remote Desktop to see the Mac’s screen and control it with your fingers.'**
  String get settingsDeskNote;

  /// Settings › Remote Desktop: Mac login passwords saved for unlocking.
  ///
  /// In en, this message translates to:
  /// **'Saved Mac Passwords'**
  String get settingsDeskPasswords;

  /// Settings › Saved Mac Passwords: subtitle counting saved passwords.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {Login password saved for {count} Mac} other {Login passwords saved for {count} Macs}}'**
  String settingsDeskPasswordsCount(int count);

  /// Settings › Saved Mac Passwords: button that removes them all.
  ///
  /// In en, this message translates to:
  /// **'Forget All'**
  String get settingsDeskPasswordsForget;

  /// Settings › Saved Mac Passwords: subtitle when none are saved.
  ///
  /// In en, this message translates to:
  /// **'No saved passwords'**
  String get settingsDeskPasswordsNone;

  /// Settings › Remote Desktop: control mode where the screen acts as a trackpad.
  ///
  /// In en, this message translates to:
  /// **'Trackpad'**
  String get settingsDeskTrackpad;

  /// Settings › Remote Desktop: note for Trackpad.
  ///
  /// In en, this message translates to:
  /// **'Use the phone screen as a trackpad: slide one finger to move the pointer; taps and right-clicks land on the pointer; slide three fingers to drag.'**
  String get settingsDeskTrackpadNote;

  /// Settings › Chat: switch.
  ///
  /// In en, this message translates to:
  /// **'Expand Tool Calls by Default'**
  String get settingsExpandTools;

  /// Settings › Terminal: note under the font size.
  ///
  /// In en, this message translates to:
  /// **'On the phone, the terminal is laid out for the phone’s screen. Smaller text fits more on a line.'**
  String get settingsFontNote;

  /// Settings › Terminal: sample text drawn at the chosen font size.
  ///
  /// In en, this message translates to:
  /// **'the agent is running Claude Code'**
  String get settingsFontSample;

  /// Settings › Terminal: font size slider label.
  ///
  /// In en, this message translates to:
  /// **'Font Size'**
  String get settingsFontSize;

  /// Settings: section title for the home screen.
  ///
  /// In en, this message translates to:
  /// **'Home'**
  String get settingsHome;

  /// Settings › Terminal: row that opens the key bar settings.
  ///
  /// In en, this message translates to:
  /// **'Shortcuts'**
  String get settingsKeys;

  /// Settings › Terminal › Shortcuts: subtitle counting custom keys.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} custom key} other {{count} custom keys}}'**
  String settingsKeysCustom(int count);

  /// Settings › Terminal › Shortcuts: subtitle when there are no custom keys.
  ///
  /// In en, this message translates to:
  /// **'Reorder the key bar above the keyboard and add your own keys'**
  String get settingsKeysNote;

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

  /// Settings › Mac: row title when no Mac is connected.
  ///
  /// In en, this message translates to:
  /// **'Not Connected'**
  String get settingsNotConnected;

  /// Settings › Open Sessions In: chat view.
  ///
  /// In en, this message translates to:
  /// **'Chat'**
  String get settingsOpenChat;

  /// Settings › Open Sessions In: note when Chat is chosen.
  ///
  /// In en, this message translates to:
  /// **'You can switch to the terminal at the top right of the chat. Agents without a saved session and shells always open in the terminal.'**
  String get settingsOpenChatNote;

  /// Settings: section title choosing whether a session opens in chat or terminal.
  ///
  /// In en, this message translates to:
  /// **'Open Sessions In'**
  String get settingsOpenFirst;

  /// Settings › Open Sessions In: terminal view.
  ///
  /// In en, this message translates to:
  /// **'Terminal'**
  String get settingsOpenTerminal;

  /// Settings › Open Sessions In: note when Terminal is chosen.
  ///
  /// In en, this message translates to:
  /// **'You can switch to the chat at the top right of the terminal. Chat and terminal are two views of the same session; switching replaces one with the other.'**
  String get settingsOpenTerminalNote;

  /// Settings › Mac: subtitle counting paired Macs.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} Mac paired} other {{count} Macs paired}}'**
  String settingsPairedCount(int count);

  /// Settings › Home: switch.
  ///
  /// In en, this message translates to:
  /// **'Show Branch and Path for Projects'**
  String get settingsProjectMeta;

  /// Settings › Notifications: note when the system permission is denied.
  ///
  /// In en, this message translates to:
  /// **'Notifications are off for Posse. Turn them on in the iPhone’s Settings › Posse › Notifications.'**
  String get settingsPushDenied;

  /// Settings › Notifications: switch for notifications when an agent finishes a turn the user hasn't seen.
  ///
  /// In en, this message translates to:
  /// **'When an agent finishes'**
  String get settingsPushDone;

  /// Settings › Notifications: note when registering for push failed.
  ///
  /// In en, this message translates to:
  /// **'Couldn’t sign up for notifications. Try again after reconnecting.'**
  String get settingsPushFailed;

  /// Settings › Notifications: note when the connected Mac has no relay.
  ///
  /// In en, this message translates to:
  /// **'This Mac has no relay set up, so it can’t send notifications. Add one under Phone › Relay in Posse on the Mac.'**
  String get settingsPushNoRelay;

  /// Settings › Notifications: note when both switches are off.
  ///
  /// In en, this message translates to:
  /// **'No notifications.'**
  String get settingsPushOff;

  /// Settings › Notifications: note when the Mac doesn't support push.
  ///
  /// In en, this message translates to:
  /// **'Posse on this Mac is too old to send notifications. Update it first.'**
  String get settingsPushOldComputer;

  /// Settings › Notifications: note when push is working.
  ///
  /// In en, this message translates to:
  /// **'The Mac notifies this phone while Posse is in the background and nobody is using the Mac.'**
  String get settingsPushOn;

  /// Settings: section title for push notifications.
  ///
  /// In en, this message translates to:
  /// **'Notifications'**
  String get settingsPushTitle;

  /// Settings: section title for staying connected while the app is in the background (iOS).
  ///
  /// In en, this message translates to:
  /// **'Background'**
  String get settingsBackgroundTitle;

  /// Settings › Background: switch that keeps the connection alive indefinitely while the app is in the background.
  ///
  /// In en, this message translates to:
  /// **'Stay Connected in Background'**
  String get settingsBackgroundKeep;

  /// Settings › Background: note when the switch is off.
  ///
  /// In en, this message translates to:
  /// **'After you switch away, the connection stays for about 30 seconds; come back within that time and nothing reconnects.'**
  String get settingsBackgroundKeepOff;

  /// Settings › Background: note when the switch is on.
  ///
  /// In en, this message translates to:
  /// **'Plays silent audio mixed with other apps so iOS doesn’t suspend Posse. Terminals keep streaming in the background, which uses more battery; iOS can still close the app when memory is low.'**
  String get settingsBackgroundKeepOn;

  /// Settings › Notifications: note before the phone has connected to a Mac.
  ///
  /// In en, this message translates to:
  /// **'Takes effect once connected to a Mac.'**
  String get settingsPushUnknown;

  /// Settings › Notifications: switch for notifications when an agent waits for the user.
  ///
  /// In en, this message translates to:
  /// **'When an agent needs you'**
  String get settingsPushWaiting;

  /// Settings › Chat: switch.
  ///
  /// In en, this message translates to:
  /// **'Show Thinking'**
  String get settingsShowReasoning;

  /// Settings: section title for the terminal.
  ///
  /// In en, this message translates to:
  /// **'Terminal'**
  String get settingsTerminal;

  /// Settings › Appearance: dark theme.
  ///
  /// In en, this message translates to:
  /// **'Dark'**
  String get settingsThemeDark;

  /// Settings › Appearance: light theme.
  ///
  /// In en, this message translates to:
  /// **'Light'**
  String get settingsThemeLight;

  /// Settings › Appearance: follow the phone’s appearance.
  ///
  /// In en, this message translates to:
  /// **'System'**
  String get settingsThemeSystem;

  /// Settings page title.
  ///
  /// In en, this message translates to:
  /// **'Settings'**
  String get settingsTitle;

  /// Settings › About: result of Check for Updates.
  ///
  /// In en, this message translates to:
  /// **'Up to Date'**
  String get settingsUpToDate;

  /// Settings › About: row that opens the update sheet.
  ///
  /// In en, this message translates to:
  /// **'Update to {version} ({build})'**
  String settingsUpdateTo(String version, String build);

  /// Settings › Home: switch.
  ///
  /// In en, this message translates to:
  /// **'Show Branch for Workspaces'**
  String get settingsWorkspaceMeta;

  /// Terminal: notice when a tapped link can’t be opened.
  ///
  /// In en, this message translates to:
  /// **'Can’t open this link: {url}'**
  String terminalCantOpenLink(String url);

  /// Terminal: notice after tapping Paste with nothing usable.
  ///
  /// In en, this message translates to:
  /// **'The clipboard has no text or image'**
  String get terminalClipboardEmpty;

  /// Terminal: notice when the terminal process ended.
  ///
  /// In en, this message translates to:
  /// **'The terminal has exited'**
  String get terminalExited;

  /// Terminal key bar: key cap of the key that clears the input.
  ///
  /// In en, this message translates to:
  /// **'Clear'**
  String get terminalKeyClear;

  /// Terminal: notice after tapping Exit with no agent running.
  ///
  /// In en, this message translates to:
  /// **'No agent is running in this terminal'**
  String get terminalNoAgent;

  /// Terminal: notice after tapping a path outside the workspace.
  ///
  /// In en, this message translates to:
  /// **'This file isn’t in the current workspace and can’t be opened on the phone'**
  String get terminalNotInWorkspace;

  /// Terminal key bar: accessibility label of the Send File key.
  ///
  /// In en, this message translates to:
  /// **'Send File'**
  String get terminalSendFile;

  /// Terminal key bar: accessibility label of the Send Now key.
  ///
  /// In en, this message translates to:
  /// **'Send Now'**
  String get terminalSendNow;

  /// Terminal More sheet: switch to the chat view.
  ///
  /// In en, this message translates to:
  /// **'Switch to Chat'**
  String get terminalSwitchToChat;

  /// Relative time of a day or more; count is days.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, one {{count} day ago} other {{count} days ago}}'**
  String timeDaysAgo(int count);

  /// Relative time under one day; count is hours.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, other {{count} hr ago}}'**
  String timeHoursAgo(int count);

  /// Relative time under one minute (history list, Plan Usage, media).
  ///
  /// In en, this message translates to:
  /// **'Just now'**
  String get timeJustNow;

  /// Relative time under one hour; count is minutes.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, other {{count} min ago}}'**
  String timeMinutesAgo(int count);

  /// Update sheet (Android): note under the button.
  ///
  /// In en, this message translates to:
  /// **'When the download finishes, the system asks you to confirm installing over the current app. Your pairings are kept. The first time, allow Posse to install apps in Settings.'**
  String get updateAndroidNote;

  /// Update sheet (Android): error when the downloaded package is in an unexpected place.
  ///
  /// In en, this message translates to:
  /// **'Can’t open the installer: the package isn’t in the update folder'**
  String get updateBadPackage;

  /// Home update banner. version and build are the new release’s.
  ///
  /// In en, this message translates to:
  /// **'New version {version} ({build})'**
  String updateBanner(String version, String build);

  /// Update sheet: build number of the new release.
  ///
  /// In en, this message translates to:
  /// **'Build {build}'**
  String updateBuild(String build);

  /// Update sheet: install button.
  ///
  /// In en, this message translates to:
  /// **'Update'**
  String get updateButton;

  /// Update sheet (Android): error when the system installer can’t be opened. {detail} is the reason the system gave, shown as-is.
  ///
  /// In en, this message translates to:
  /// **'Can’t open the installer: {detail}'**
  String updateCantInstall(String detail);

  /// Update sheet: the installed version.
  ///
  /// In en, this message translates to:
  /// **'Current {version} ({build})'**
  String updateCurrent(String version, String build);

  /// Update sheet: error when the download fails.
  ///
  /// In en, this message translates to:
  /// **'Download failed. Try again later.'**
  String get updateDownloadFailed;

  /// Update sheet (Android): button label while downloading.
  ///
  /// In en, this message translates to:
  /// **'Downloading…'**
  String get updateDownloading;

  /// Update sheet (Android): button label with progress.
  ///
  /// In en, this message translates to:
  /// **'Downloading {percent}%'**
  String updateDownloadingPercent(int percent);

  /// Update sheet (Android): error when installing apps isn’t allowed.
  ///
  /// In en, this message translates to:
  /// **'Allow Posse to install apps in Settings to update'**
  String get updateInstallDenied;

  /// Update sheet (iOS): note under the button.
  ///
  /// In en, this message translates to:
  /// **'After you tap Update, it downloads in the background and the Home Screen icon shows the progress. When it’s installed, the app switches to the new version. Your pairings are kept.'**
  String get updateIosNote;

  /// Update sheet (Android): button label when the download finished.
  ///
  /// In en, this message translates to:
  /// **'Opening installer…'**
  String get updateOpening;

  /// Update sheet (iOS): button label while asking the system to install.
  ///
  /// In en, this message translates to:
  /// **'Requesting installation…'**
  String get updateRequesting;

  /// Home update banner: tooltip of the dismiss button.
  ///
  /// In en, this message translates to:
  /// **'Don’t remind me about this version'**
  String get updateSkip;

  /// Update sheet title.
  ///
  /// In en, this message translates to:
  /// **'New Version {version}'**
  String updateTitle(String version);

  /// Upload progress bar while sending files. index is the current one (from 1), count the total.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, =0 {Sending file} =1 {Sending file} other {Sending file {index}/{count}}}'**
  String uploadSendingFile(int count, int index);

  /// Upload progress bar while sending images. index is the current one (from 1), count the total.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, =0 {Sending image} =1 {Sending image} other {Sending image {index}/{count}}}'**
  String uploadSendingImage(int count, int index);

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

  /// Plan Usage sheet: second line under a pool’s reset time, the age of that pool’s own reading.
  ///
  /// In en, this message translates to:
  /// **'Reading from {time}'**
  String usageReadingFrom(String time);

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

  /// Plan Usage: the window’s reset time has passed.
  ///
  /// In en, this message translates to:
  /// **'Already reset'**
  String get usageResetDone;

  /// Plan Usage: when a window resets, a day or more away; date is a date and time such as Oct 23 18:04.
  ///
  /// In en, this message translates to:
  /// **'Resets {date}'**
  String usageResetsAt(String date);

  /// Plan Usage: when a window resets, whole hours under a day; count is hours.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, other {Resets in {count} hr}}'**
  String usageResetsInHours(int count);

  /// Plan Usage: when a window resets, under a day; hours and minutes are numbers.
  ///
  /// In en, this message translates to:
  /// **'Resets in {hours} hr {minutes} min'**
  String usageResetsInHoursMinutes(int hours, int minutes);

  /// Plan Usage: when a window resets, under one hour; count is minutes.
  ///
  /// In en, this message translates to:
  /// **'{count, plural, other {Resets in {count} min}}'**
  String usageResetsInMinutes(int count);

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

  /// Name of the workspace that is the project folder itself.
  ///
  /// In en, this message translates to:
  /// **'Main Workspace'**
  String get workspaceMain;

  /// Workspace status when it has no sessions.
  ///
  /// In en, this message translates to:
  /// **'No Sessions'**
  String get workspaceNoSessions;

  /// Workspace page: segment for Git changes.
  ///
  /// In en, this message translates to:
  /// **'Changes'**
  String get workspaceTabChanges;

  /// Workspace page: segment for the file browser.
  ///
  /// In en, this message translates to:
  /// **'Files'**
  String get workspaceTabFiles;

  /// Workspace page: segment for session history.
  ///
  /// In en, this message translates to:
  /// **'History'**
  String get workspaceTabHistory;

  /// Mobile discussions: Discussions
  ///
  /// In en, this message translates to:
  /// **'Discussions'**
  String get discTitle;

  /// Mobile discussions: New discussion
  ///
  /// In en, this message translates to:
  /// **'New discussion'**
  String get discNew;

  /// Mobile discussions: Join discussion
  ///
  /// In en, this message translates to:
  /// **'Join discussion'**
  String get discJoin;

  /// Mobile discussions: No discussions yet
  ///
  /// In en, this message translates to:
  /// **'No discussions yet'**
  String get discEmpty;

  /// Mobile discussions: Archived
  ///
  /// In en, this message translates to:
  /// **'Archived'**
  String get discArchived;

  /// Mobile discussions: Name
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get discName;

  /// Mobile discussions: Description
  ///
  /// In en, this message translates to:
  /// **'Description'**
  String get discDescription;

  /// Mobile discussions: Workspace on this computer
  ///
  /// In en, this message translates to:
  /// **'Workspace on this computer'**
  String get discWorkspace;

  /// Mobile discussions: Save
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get discSave;

  /// Mobile discussions: Cancel
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get discCancel;

  /// Mobile discussions: Retry
  ///
  /// In en, this message translates to:
  /// **'Retry'**
  String get discRetry;

  /// Mobile discussions: Close
  ///
  /// In en, this message translates to:
  /// **'Close'**
  String get discClose;

  /// Mobile discussions: Invitation
  ///
  /// In en, this message translates to:
  /// **'Invitation'**
  String get discInviteCode;

  /// Mobile discussions: Search messages
  ///
  /// In en, this message translates to:
  /// **'Search messages'**
  String get discSearch;

  /// Mobile discussions: Discussion details
  ///
  /// In en, this message translates to:
  /// **'Discussion details'**
  String get discDetails;

  /// Mobile discussions: Agents
  ///
  /// In en, this message translates to:
  /// **'Agents'**
  String get discMembers;

  /// Mobile discussions: Add agent
  ///
  /// In en, this message translates to:
  /// **'Add agent'**
  String get discAddMember;

  /// Mobile discussions: Role
  ///
  /// In en, this message translates to:
  /// **'Role'**
  String get discRole;

  /// Mobile discussions: Agent
  ///
  /// In en, this message translates to:
  /// **'Agent'**
  String get discAgent;

  /// Mobile discussions: Base branch (optional)
  ///
  /// In en, this message translates to:
  /// **'Base branch (optional)'**
  String get discBase;

  /// Mobile discussions: Restart worker with these settings
  ///
  /// In en, this message translates to:
  /// **'Restart worker with these settings'**
  String get discApplyNow;

  /// Mobile discussions: Remove
  ///
  /// In en, this message translates to:
  /// **'Remove'**
  String get discRemove;

  /// Mobile discussions: Restart and continue
  ///
  /// In en, this message translates to:
  /// **'Restart and continue'**
  String get discRestart;

  /// Mobile discussions: Continue queued requests
  ///
  /// In en, this message translates to:
  /// **'Continue queued requests'**
  String get discResume;

  /// Mobile discussions: Owner
  ///
  /// In en, this message translates to:
  /// **'Owner'**
  String get discOwner;

  /// Mobile discussions: Key messages
  ///
  /// In en, this message translates to:
  /// **'Key messages'**
  String get discKey;

  /// Mobile discussions: Mark as key
  ///
  /// In en, this message translates to:
  /// **'Mark as key'**
  String get discMarkKey;

  /// Mobile discussions: Unmark as key
  ///
  /// In en, this message translates to:
  /// **'Unmark as key'**
  String get discUnmarkKey;

  /// Mobile discussions: Quote
  ///
  /// In en, this message translates to:
  /// **'Quote'**
  String get discQuote;

  /// Mobile discussions: Answer
  ///
  /// In en, this message translates to:
  /// **'Answer'**
  String get discAnswer;

  /// Mobile discussions: Send
  ///
  /// In en, this message translates to:
  /// **'Send'**
  String get discSend;

  /// Mobile discussions: Message the discussion…
  ///
  /// In en, this message translates to:
  /// **'Message the discussion…'**
  String get discMessageHint;

  /// Mobile discussions: Message only
  ///
  /// In en, this message translates to:
  /// **'Message only'**
  String get discNoTarget;

  /// Mobile discussions: Ask an agent
  ///
  /// In en, this message translates to:
  /// **'Ask an agent'**
  String get discTarget;

  /// Mobile discussions: Mention a person
  ///
  /// In en, this message translates to:
  /// **'Mention a person'**
  String get discMention;

  /// Mobile discussions: Load earlier messages
  ///
  /// In en, this message translates to:
  /// **'Load earlier messages'**
  String get discEarlier;

  /// Mobile discussions: Load newer messages
  ///
  /// In en, this message translates to:
  /// **'Load newer messages'**
  String get discLater;

  /// Mobile discussions: Jump to latest
  ///
  /// In en, this message translates to:
  /// **'Jump to latest'**
  String get discLatest;

  /// Mobile discussions: New messages
  ///
  /// In en, this message translates to:
  /// **'New messages'**
  String get discNewMessages;

  /// Mobile discussions: This discussion is archived
  ///
  /// In en, this message translates to:
  /// **'This discussion is archived'**
  String get discReadOnly;

  /// Mobile discussions: Disconnected. Your draft is kept.
  ///
  /// In en, this message translates to:
  /// **'Disconnected. Your draft is kept.'**
  String get discOffline;

  /// Mobile discussions: Waiting for the host to approve
  ///
  /// In en, this message translates to:
  /// **'Waiting for the host to approve'**
  String get discPending;

  /// Mobile discussions: You no longer have access to this discussion
  ///
  /// In en, this message translates to:
  /// **'You no longer have access to this discussion'**
  String get discRevoked;

  /// Mobile discussions: People
  ///
  /// In en, this message translates to:
  /// **'People'**
  String get discPeople;

  /// Mobile discussions: Share on LAN
  ///
  /// In en, this message translates to:
  /// **'Share on LAN'**
  String get discShare;

  /// Mobile discussions: Stop LAN sharing
  ///
  /// In en, this message translates to:
  /// **'Stop LAN sharing'**
  String get discUnshare;

  /// Mobile discussions: Create invitation
  ///
  /// In en, this message translates to:
  /// **'Create invitation'**
  String get discInvite;

  /// Mobile discussions: Leave discussion
  ///
  /// In en, this message translates to:
  /// **'Leave discussion'**
  String get discLeave;

  /// Mobile discussions: Approve
  ///
  /// In en, this message translates to:
  /// **'Approve'**
  String get discApprove;

  /// Mobile discussions: Reject
  ///
  /// In en, this message translates to:
  /// **'Reject'**
  String get discReject;

  /// Mobile discussions: Edit discussion
  ///
  /// In en, this message translates to:
  /// **'Edit discussion'**
  String get discRename;

  /// Mobile discussions: Archive
  ///
  /// In en, this message translates to:
  /// **'Archive'**
  String get discArchive;

  /// Mobile discussions: Restore
  ///
  /// In en, this message translates to:
  /// **'Restore'**
  String get discRestore;

  /// Mobile discussions: Stop execution
  ///
  /// In en, this message translates to:
  /// **'Stop execution'**
  String get discStop;

  /// Mobile discussions: Run in the same directory
  ///
  /// In en, this message translates to:
  /// **'Run in the same directory'**
  String get discConfirm;

  /// Mobile discussions: Use an independent worktree
  ///
  /// In en, this message translates to:
  /// **'Use an independent worktree'**
  String get discIsolate;

  /// Mobile discussions: Abandon and retry
  ///
  /// In en, this message translates to:
  /// **'Abandon and retry'**
  String get discAbandon;

  /// Mobile discussions: This request may already have changed files. Check its effects before starting it again.
  ///
  /// In en, this message translates to:
  /// **'This request may already have changed files. Check its effects before starting it again.'**
  String get discAbandonWarning;

  /// Mobile discussions: Another agent may be changing files in this directory. Continue only after checking for conflicts.
  ///
  /// In en, this message translates to:
  /// **'Another agent may be changing files in this directory. Continue only after checking for conflicts.'**
  String get discConcurrentWarning;

  /// Mobile discussions: Open worker terminal
  ///
  /// In en, this message translates to:
  /// **'Open worker terminal'**
  String get discOpenWorker;

  /// Mobile discussions: Attachments
  ///
  /// In en, this message translates to:
  /// **'Attachments'**
  String get discAttachments;

  /// Mobile discussions: Add a file
  ///
  /// In en, this message translates to:
  /// **'Add a file'**
  String get discAddFile;

  /// Mobile discussions: Add a photo
  ///
  /// In en, this message translates to:
  /// **'Add a photo'**
  String get discAddPhoto;

  /// Mobile discussions: Each attachment must be 20 MiB or smaller
  ///
  /// In en, this message translates to:
  /// **'Each attachment must be 20 MiB or smaller'**
  String get discFileLarge;

  /// Mobile discussions: Up to 10 attachments per message
  ///
  /// In en, this message translates to:
  /// **'Up to 10 attachments per message'**
  String get discFileLimit;

  /// Mobile discussions: The attachment download was incomplete
  ///
  /// In en, this message translates to:
  /// **'The attachment download was incomplete'**
  String get discDownloadError;

  /// Mobile discussions: The result is not confirmed. Retry the same request to avoid duplicates.
  ///
  /// In en, this message translates to:
  /// **'The result is not confirmed. Retry the same request to avoid duplicates.'**
  String get discUnknown;

  /// Mobile discussions: Edit as a new message
  ///
  /// In en, this message translates to:
  /// **'Edit as a new message'**
  String get discEditDraft;

  /// Mobile discussions: The previous message may already have been sent. Check the discussion before sending a new message.
  ///
  /// In en, this message translates to:
  /// **'The previous message may already have been sent. Check the discussion before sending a new message.'**
  String get discEditDraftWarning;

  /// Mobile discussions: Uploading attachment…
  ///
  /// In en, this message translates to:
  /// **'Uploading attachment…'**
  String get discUploading;

  /// Mobile discussions: Sending…
  ///
  /// In en, this message translates to:
  /// **'Sending…'**
  String get discSending;

  /// Mobile discussions: Online
  ///
  /// In en, this message translates to:
  /// **'Online'**
  String get discOnline;

  /// Mobile discussions: Offline
  ///
  /// In en, this message translates to:
  /// **'Offline'**
  String get discPersonOffline;

  /// Mobile discussions: No messages yet
  ///
  /// In en, this message translates to:
  /// **'No messages yet'**
  String get discNoMessages;

  /// Mobile discussions: Unread
  ///
  /// In en, this message translates to:
  /// **'Unread'**
  String get discUnread;

  /// Mobile discussions: Needs your answer
  ///
  /// In en, this message translates to:
  /// **'Needs your answer'**
  String get discWaiting;

  /// Mobile discussions: Queued
  ///
  /// In en, this message translates to:
  /// **'Queued'**
  String get discStateQueued;

  /// Mobile discussions: Needs confirmation
  ///
  /// In en, this message translates to:
  /// **'Needs confirmation'**
  String get discStateHeld;

  /// Mobile discussions: Blocked
  ///
  /// In en, this message translates to:
  /// **'Blocked'**
  String get discStateBlocked;

  /// Mobile discussions: Starting
  ///
  /// In en, this message translates to:
  /// **'Starting'**
  String get discStateStarting;

  /// Mobile discussions: Running
  ///
  /// In en, this message translates to:
  /// **'Running'**
  String get discStateRunning;

  /// Mobile discussions: Waiting for an answer
  ///
  /// In en, this message translates to:
  /// **'Waiting for an answer'**
  String get discStateWaiting;

  /// Mobile discussions: Done
  ///
  /// In en, this message translates to:
  /// **'Done'**
  String get discStateDone;

  /// Mobile discussions: Failed
  ///
  /// In en, this message translates to:
  /// **'Failed'**
  String get discStateFailed;

  /// Mobile discussions: Interrupted
  ///
  /// In en, this message translates to:
  /// **'Interrupted'**
  String get discStateInterrupted;

  /// Mobile discussions: Stopped
  ///
  /// In en, this message translates to:
  /// **'Stopped'**
  String get discStateStopped;

  /// Mobile discussions: Cancelled
  ///
  /// In en, this message translates to:
  /// **'Cancelled'**
  String get discStateCancelled;

  /// Mobile discussions: Claimed
  ///
  /// In en, this message translates to:
  /// **'Claimed'**
  String get discStateClaimed;

  /// Mobile discussions: Revoked
  ///
  /// In en, this message translates to:
  /// **'Revoked'**
  String get discStateRevoked;

  /// Mobile discussions: Select the attachment again to retry this message. Its request ID will be kept.
  ///
  /// In en, this message translates to:
  /// **'Select the attachment again to retry this message. Its request ID will be kept.'**
  String get discDraftFilesExpired;

  /// Open the report attached to a discussion reply
  ///
  /// In en, this message translates to:
  /// **'Read report'**
  String get discReport;

  /// Discard an uncertain member form and explicitly start a new request
  ///
  /// In en, this message translates to:
  /// **'Start a new request'**
  String get discNewRequest;

  /// Confirmation before replacing an uncertain member request
  ///
  /// In en, this message translates to:
  /// **'The previous request may already have added an agent or created a worktree. Check its result before starting again.'**
  String get discNewRequestWarning;

  /// Mobile discussions: @ candidate for someone else's agent you can't use; picking it reminds its owner
  ///
  /// In en, this message translates to:
  /// **'Asks {owner}'**
  String discAtOthersAgent(String owner);

  /// Mobile discussions: list badge when someone mentioned you
  ///
  /// In en, this message translates to:
  /// **'@You'**
  String get discAtYou;

  /// Mobile discussions: member form, who may send requests to this agent
  ///
  /// In en, this message translates to:
  /// **'Who can @ it'**
  String get discAccessLabel;

  /// Mobile discussions: access option
  ///
  /// In en, this message translates to:
  /// **'Only me'**
  String get discAccessOnlyMe;

  /// Mobile discussions: access option
  ///
  /// In en, this message translates to:
  /// **'Everyone'**
  String get discAccessAll;

  /// Mobile discussions: access option
  ///
  /// In en, this message translates to:
  /// **'Specific people'**
  String get discAccessSome;

  /// Mobile discussions: access option has no people to pick
  ///
  /// In en, this message translates to:
  /// **'No one else has joined yet.'**
  String get discAccessNobody;

  /// Mobile discussions: member subtitle
  ///
  /// In en, this message translates to:
  /// **'Everyone can @ it'**
  String get discAccessEveryone;

  /// Mobile discussions: member subtitle listing people allowed to use the agent
  ///
  /// In en, this message translates to:
  /// **'{names} can @ it'**
  String discAccessPeople(String names);

  /// Mobile discussions: marks yourself in the people list
  ///
  /// In en, this message translates to:
  /// **'You'**
  String get discYou;

  /// Mobile discussions: marks the host in the people list
  ///
  /// In en, this message translates to:
  /// **'Host'**
  String get discHost;

  /// Mobile discussions: copy a message's text
  ///
  /// In en, this message translates to:
  /// **'Copy'**
  String get discCopy;

  /// Mobile discussions: member subtitle for someone else's agent
  ///
  /// In en, this message translates to:
  /// **'On {name}\'s computer'**
  String discOnComputer(String name);

  /// Profile screen title and the settings section title: the user's name and avatar.
  ///
  /// In en, this message translates to:
  /// **'Name and Avatar'**
  String get profileTitle;

  /// Profile screen: note that the profile belongs to the currently connected computer.
  ///
  /// In en, this message translates to:
  /// **'This changes the profile of the connected computer, {computer}. It\'s used in all of its discussions; agents keep their own names and icons.'**
  String profileNote(String computer);

  /// Settings: subtitle of the profile row.
  ///
  /// In en, this message translates to:
  /// **'Used in all discussions on {computer}'**
  String profileRowNote(String computer);

  /// Profile screen: label of the name field.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get profileName;

  /// Profile screen: the name is empty or longer than 60 characters.
  ///
  /// In en, this message translates to:
  /// **'Name must be 1–60 characters'**
  String get profileNameInvalid;

  /// Profile screen: pick an avatar from the photo library.
  ///
  /// In en, this message translates to:
  /// **'Choose Image'**
  String get profileChoose;

  /// Profile screen: remove the avatar (falls back to the first letter of the name).
  ///
  /// In en, this message translates to:
  /// **'Remove Avatar'**
  String get profileRemove;

  /// Profile screen: hint below the preview after picking an image, before saving.
  ///
  /// In en, this message translates to:
  /// **'Cropped to a square from the center after saving.'**
  String get profileCrop;

  /// Profile screen: save the name and avatar.
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get profileSave;

  /// Profile screen: section listing joined discussions whose host hasn't received the new profile.
  ///
  /// In en, this message translates to:
  /// **'Not Yet Updated'**
  String get profilePendingTitle;

  /// Profile screen: a joined discussion whose host has not received the new name or avatar. title is the discussion, reason why.
  ///
  /// In en, this message translates to:
  /// **'Not yet in “{title}”: {reason}'**
  String profilePending(String title, String reason);

  /// Reason in profilePending: the host is offline.
  ///
  /// In en, this message translates to:
  /// **'sent when the host is back online'**
  String get profileOffline;

  /// Reason in profilePending: the host runs an old version.
  ///
  /// In en, this message translates to:
  /// **'the host\'s Posse is too old'**
  String get profileUnsupported;

  /// Profile screen: the picked image is too large to send to the computer.
  ///
  /// In en, this message translates to:
  /// **'This image is too large. Choose a smaller one.'**
  String get profileImageTooLarge;

  /// Mobile discussions: title of the page that edits one of your agent members
  ///
  /// In en, this message translates to:
  /// **'Edit Agent'**
  String get discEditMember;

  /// Mobile discussions: confirm button on the add-agent page
  ///
  /// In en, this message translates to:
  /// **'Add'**
  String get discAdd;

  /// Mobile discussions: shown in the agent picker when the computer reports no agents
  ///
  /// In en, this message translates to:
  /// **'No agents on this computer'**
  String get discNoAgents;

  /// Mobile discussions: placeholder of the agent member's role field
  ///
  /// In en, this message translates to:
  /// **'What it is responsible for'**
  String get discRoleHint;

  /// Mobile discussions: label of the agent member's model field
  ///
  /// In en, this message translates to:
  /// **'Model'**
  String get discModelLabel;

  /// Mobile discussions: label of the agent member's reasoning effort field
  ///
  /// In en, this message translates to:
  /// **'Reasoning effort'**
  String get discEffortLabel;

  /// Mobile discussions: placeholder of the model and reasoning effort fields; empty means the agent's default
  ///
  /// In en, this message translates to:
  /// **'Default'**
  String get discDefault;

  /// Mobile discussions: label of where the agent member works
  ///
  /// In en, this message translates to:
  /// **'Working directory'**
  String get discWorkDir;

  /// Mobile discussions: segment, the agent works in an existing workspace
  ///
  /// In en, this message translates to:
  /// **'Existing workspace'**
  String get discExistingWorkspace;

  /// Mobile discussions: segment, create a new git worktree for the agent
  ///
  /// In en, this message translates to:
  /// **'New worktree'**
  String get discNewWorktreeMode;

  /// Mobile discussions: search found nothing
  ///
  /// In en, this message translates to:
  /// **'No matching messages'**
  String get discNoHits;

  /// Mobile discussions: empty discussion without any agent members, above the add-agent button
  ///
  /// In en, this message translates to:
  /// **'No agents yet'**
  String get discNoMembers;

  /// Mobile discussions: input placeholder while answering an agent's question
  ///
  /// In en, this message translates to:
  /// **'Your answer…'**
  String get discAnswerHint;

  /// Mobile discussions: tooltip of the button that inserts @ into the message input
  ///
  /// In en, this message translates to:
  /// **'Mention'**
  String get discAtButton;

  /// Mobile discussions: chip in the input while answering the question an agent asked
  ///
  /// In en, this message translates to:
  /// **'Answer {name}'**
  String discAnswerTo(String name);

  /// Mobile discussions: label of the new worktree's name on the agent member page
  ///
  /// In en, this message translates to:
  /// **'Worktree name'**
  String get discWorktreeName;

  /// Mobile discussions: confirm button of the new discussion dialog
  ///
  /// In en, this message translates to:
  /// **'Create'**
  String get discCreate;

  /// Mobile discussions: confirm button of the join-by-invite dialog
  ///
  /// In en, this message translates to:
  /// **'Join'**
  String get discJoinConfirm;

  /// Mobile discussions: confirm button when turning on LAN sharing
  ///
  /// In en, this message translates to:
  /// **'Share'**
  String get discShareConfirm;

  /// Mobile discussions: confirm button when turning off LAN sharing
  ///
  /// In en, this message translates to:
  /// **'Stop'**
  String get discUnshareConfirm;
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
