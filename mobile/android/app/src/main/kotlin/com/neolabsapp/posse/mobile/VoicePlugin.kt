package com.neolabsapp.posse.mobile

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioRecord
import android.media.AudioRecordingConfiguration
import android.media.MediaRecorder
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.util.Base64
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import org.json.JSONObject
import java.io.File
import java.io.RandomAccessFile
import java.security.KeyStore
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlin.math.log10
import kotlin.math.sqrt

/// 手机端语音录音与系统离线识别通道（posse/voice + posse/voice/events + posse/voice/secret）。
class VoicePlugin(
    private val activity: Activity,
    messenger: BinaryMessenger,
    private val requestMicPermission: ((Boolean) -> Unit) -> Unit,
) : EventChannel.StreamHandler {
    private val mainHandler = Handler(Looper.getMainLooper())
    private val methodChannel = MethodChannel(messenger, "posse/voice")
    private val eventChannel = EventChannel(messenger, "posse/voice/events")
    private val secretStore = VoiceSecretStore(activity.applicationContext, messenger)

    @Volatile
    private var eventSink: EventChannel.EventSink? = null

    private var activeSessionId: String? = null
    private var pendingStartSessionId: String? = null
    private var pendingStartResult: MethodChannel.Result? = null
    private var mediaRecorder: MediaRecorder? = null
    private var mediaRecorderCallback: AudioManager.AudioRecordingCallback? = null
    private var wavJob: WavRecordingJob? = null
    private var closingWavJob: WavRecordingJob? = null
    private var recordingPath: String? = null
    private var recordingStartedAt: Long = 0
    private var meterRunnable: Runnable? = null
    private var maxLimitRunnable: Runnable? = null

    private var speechRecognizer: SpeechRecognizer? = null
    private var nativeActive = false
    private var nativeFinishing = false
    private var nativeFinishFallback: Runnable? = null
    private var latestTranscript = ""

    init {
        methodChannel.setMethodCallHandler { call, result ->
            try {
                handle(call, result)
            } catch (e: Exception) {
                result.error("failed", e.message ?: "voice plugin error", null)
            }
        }
        eventChannel.setStreamHandler(this)
    }

    override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
        eventSink = events
    }

    override fun onCancel(arguments: Any?) {
        eventSink = null
    }

    private fun emit(payload: Map<String, Any?>) {
        mainHandler.post { eventSink?.success(payload) }
    }

    /// Activity 真正进入后台（onStop）时作废待启动请求、安全停录并通知 Dart 保留音频重试，不丢话。
    fun onAppBackgrounded() {
        mainHandler.post {
            cancelPendingStart(null, "background")
            interruptActiveCapture(reason = "background")
        }
    }

    fun dispose() {
        cancelPendingStart(null, "disposed")
        stopRecordingAsync(sessionId = null, deleteFile = true) {}
        stopNativeRecognition(sessionId = null, cancel = true)
        methodChannel.setMethodCallHandler(null)
        eventChannel.setStreamHandler(null)
        secretStore.dispose()
    }

    private fun cancelPendingStart(sessionId: String?, reason: String) {
        val pendingId = pendingStartSessionId ?: return
        if (!sessionId.isNullOrEmpty() && pendingId != sessionId) return
        val res = pendingStartResult
        pendingStartSessionId = null
        pendingStartResult = null
        res?.error("cancelled", reason, null)
    }

    /// 若上一笔 WAV 录音正在关闭释放麦克风，先在主线程登记可取消的待启动会话，
    /// 仅在旧硬件确认释放且本次待启动未被取消/抢占时才放行启动。
    private fun runAfterMicReleased(
        sessionId: String,
        result: MethodChannel.Result,
        action: () -> Unit,
    ) {
        cancelPendingStart(null, "preempted")
        val closing = closingWavJob
        if (closing == null) {
            action()
            return
        }
        pendingStartSessionId = sessionId
        pendingStartResult = result
        Thread {
            val released = closing.awaitMicReleased(2000)
            mainHandler.post {
                if (pendingStartSessionId != sessionId) return@post
                pendingStartSessionId = null
                pendingStartResult = null
                if (!released) {
                    result.error("mic_busy", "previous microphone session is still closing", null)
                    return@post
                }
                if (closingWavJob === closing) closingWavJob = null
                action()
            }
        }.start()
    }

    private fun isOnDeviceSpeechSupported(ctx: Context): Boolean {
        return Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
            SpeechRecognizer.isOnDeviceRecognitionAvailable(ctx)
    }

    private fun handle(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "capabilities" -> {
                val ctx = activity.applicationContext
                // 仅当系统明确支持本机离线识别时报告可用，绝不把音频静默送给云端识别服务。
                val onDeviceAvailable = isOnDeviceSpeechSupported(ctx)
                result.success(
                    mapOf(
                        "nativeAvailable" to onDeviceAvailable,
                        "onDeviceAvailable" to onDeviceAvailable,
                        "maxNativeSeconds" to 60,
                    ),
                )
            }
            "authorize" -> {
                val needSpeech = call.argument<Boolean>("needSpeech") ?: false
                if (activity.checkSelfPermission(Manifest.permission.RECORD_AUDIO) ==
                    PackageManager.PERMISSION_GRANTED
                ) {
                    result.success(
                        mapOf(
                            "mic" to "granted",
                            "speech" to if (needSpeech) "granted" else "not_needed",
                        ),
                    )
                    return
                }
                requestMicPermission { granted ->
                    val state = if (granted) "granted" else "denied"
                    result.success(
                        mapOf(
                            "mic" to state,
                            "speech" to if (needSpeech) state else "not_needed",
                        ),
                    )
                }
            }
            "openSettings" -> {
                val intent = Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
                    data = Uri.fromParts("package", activity.packageName, null)
                }
                activity.startActivity(intent)
                result.success(true)
            }
            "startRecording" -> {
                val path = call.argument<String>("path")
                if (path.isNullOrEmpty()) {
                    result.error("bad_args", "missing path", null)
                    return
                }
                val sessionId = call.argument<String>("sessionId") ?: UUID.randomUUID().toString()
                val format = call.argument<String>("format") ?: "m4a"
                startRecording(sessionId, path, format, result)
            }
            "stopRecording" -> {
                val sessionId = call.argument<String>("sessionId")
                stopRecordingAsync(sessionId, deleteFile = false) { info ->
                    result.success(info)
                }
            }
            "cancelRecording" -> {
                val sessionId = call.argument<String>("sessionId")
                cancelPendingStart(sessionId, "cancelled")
                stopRecordingAsync(sessionId, deleteFile = true) {
                    result.success(null)
                }
            }
            "startNativeRecognition" -> {
                val sessionId = call.argument<String>("sessionId") ?: UUID.randomUUID().toString()
                val locale = call.argument<String>("locale")
                val hints = call.argument<List<String>>("hints") ?: emptyList()
                startNativeRecognition(sessionId, locale, hints, result)
            }
            "stopNativeRecognition" -> {
                val sessionId = call.argument<String>("sessionId")
                result.success(stopNativeRecognition(sessionId, cancel = false))
            }
            "cancelNativeRecognition" -> {
                val sessionId = call.argument<String>("sessionId")
                cancelPendingStart(sessionId, "cancelled")
                stopNativeRecognition(sessionId, cancel = true)
                result.success(null)
            }
            else -> result.notImplemented()
        }
    }

    private fun preemptActiveCapture() {
        interruptActiveCapture(reason = "preempted")
    }

    private fun interruptActiveCapture(reason: String) {
        if (mediaRecorder != null || wavJob != null) {
            val sid = activeSessionId ?: ""
            val job = wavJob
            stopRecordingAsync(sessionId = null, deleteFile = false, interruptedReason = reason) { info ->
                // 若已有同 sessionId 的 stopRecording RPC 挂接共享本次结果，由 RPC 单次交付，不再重复 emit。
                if (job != null && job.hasRpcConsumer()) return@stopRecordingAsync
                if (info != null) {
                    emit(
                        mapOf(
                            "type" to "interrupted",
                            "sessionId" to sid,
                            "path" to info["path"],
                            "durationMs" to info["durationMs"],
                            "reason" to reason,
                        ),
                    )
                } else {
                    emit(
                        mapOf(
                            "type" to "error",
                            "sessionId" to sid,
                            "code" to "record_failed",
                            "message" to reason,
                        ),
                    )
                }
            }
            return
        }
        if (nativeActive) {
            val sid = activeSessionId ?: ""
            val text = latestTranscript
            val wasFinishing = nativeFinishing
            cleanupNative()
            if (wasFinishing && text.isNotEmpty()) {
                emit(mapOf("type" to "final", "sessionId" to sid, "text" to text))
            } else {
                emit(
                    mapOf(
                        "type" to "interrupted",
                        "sessionId" to sid,
                        "text" to text,
                        "durationMs" to 0,
                        "reason" to reason,
                    ),
                )
            }
        }
    }

    // MARK: - File Recording

    private fun startRecording(
        sessionId: String,
        path: String,
        format: String,
        result: MethodChannel.Result,
    ) {
        preemptActiveCapture()
        runAfterMicReleased(sessionId, result) {
            val outFile = File(path)
            outFile.parentFile?.mkdirs()
            if (format == "wav") {
                startWavRecording(sessionId, outFile, result)
            } else {
                startM4aRecording(sessionId, outFile, result)
            }
        }
    }

    private fun startM4aRecording(
        sessionId: String,
        outFile: File,
        result: MethodChannel.Result,
    ) {
        var rec: MediaRecorder? = null
        var recCb: AudioManager.AudioRecordingCallback? = null
        try {
            rec = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                MediaRecorder(activity.applicationContext)
            } else {
                @Suppress("DEPRECATION")
                MediaRecorder()
            }
            rec.setAudioSource(MediaRecorder.AudioSource.MIC)
            rec.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
            rec.setAudioEncoder(MediaRecorder.AudioEncoder.AAC)
            rec.setAudioSamplingRate(16000)
            rec.setAudioChannels(1)
            rec.setAudioEncodingBitRate(32000)
            rec.setOutputFile(outFile.absolutePath)
            rec.setOnErrorListener { _, _, _ ->
                mainHandler.post {
                    if (activeSessionId == sessionId) {
                        interruptActiveCapture(reason = "audio_interrupted")
                    }
                }
            }
            // Android 10+ 在 MediaRecorder 实例自身注册静音回调，只感知当前录音被静音，不受其他客户端干扰。
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                val cb = object : AudioManager.AudioRecordingCallback() {
                    override fun onRecordingConfigChanged(configs: MutableList<AudioRecordingConfiguration>?) {
                        if (configs?.any { it.isClientSilenced } == true) {
                            mainHandler.post {
                                if (activeSessionId == sessionId) {
                                    interruptActiveCapture(reason = "audio_interrupted")
                                }
                            }
                        }
                    }
                }
                recCb = cb
                rec.registerAudioRecordingCallback({ r -> mainHandler.post(r) }, cb)
            }
            rec.prepare()
            rec.start()

            activeSessionId = sessionId
            mediaRecorder = rec
            mediaRecorderCallback = recCb
            recordingPath = outFile.absolutePath
            recordingStartedAt = System.currentTimeMillis()
            startMeterLoop(sessionId)
            scheduleMaxLimitGuard(sessionId)
            result.success(true)
        } catch (e: Exception) {
            releaseMediaRecorder(rec, recCb)
            outFile.delete()
            result.error("record_failed", e.message ?: "failed to start recorder", null)
        }
    }

    private fun startWavRecording(
        sessionId: String,
        outFile: File,
        result: MethodChannel.Result,
    ) {
        val sampleRate = 16000
        val channelConfig = AudioFormat.CHANNEL_IN_MONO
        val audioFormat = AudioFormat.ENCODING_PCM_16BIT
        val minBuf = AudioRecord.getMinBufferSize(sampleRate, channelConfig, audioFormat)
        if (minBuf <= 0) {
            result.error("record_failed", "unsupported audio record buffer size", null)
            return
        }
        val bufSize = maxOf(minBuf * 2, 4096)
        val audioRecord = try {
            AudioRecord(
                MediaRecorder.AudioSource.MIC,
                sampleRate,
                channelConfig,
                audioFormat,
                bufSize,
            )
        } catch (e: Exception) {
            result.error("record_failed", e.message ?: "failed to create AudioRecord", null)
            return
        }
        if (audioRecord.state != AudioRecord.STATE_INITIALIZED) {
            audioRecord.release()
            result.error("record_failed", "AudioRecord failed to initialize", null)
            return
        }

        val job = WavRecordingJob(
            sessionId = sessionId,
            outFile = outFile,
            audioRecord = audioRecord,
            sampleRate = sampleRate,
            bufSize = bufSize,
            mainHandler = mainHandler,
            onLevel = { lvl ->
                emit(mapOf("type" to "level", "sessionId" to sessionId, "level" to lvl))
            },
            onSilenced = {
                mainHandler.post {
                    if (activeSessionId == sessionId) {
                        interruptActiveCapture(reason = "audio_interrupted")
                    }
                }
            },
            onError = { msg ->
                emit(
                    mapOf(
                        "type" to "error",
                        "sessionId" to sessionId,
                        "code" to "record_failed",
                        "message" to msg,
                    ),
                )
            },
        )
        activeSessionId = sessionId
        wavJob = job
        recordingPath = outFile.absolutePath
        recordingStartedAt = System.currentTimeMillis()
        job.start()
        scheduleMaxLimitGuard(sessionId)
        result.success(true)
    }

    private fun stopRecordingAsync(
        sessionId: String?,
        deleteFile: Boolean,
        interruptedReason: String? = null,
        onDone: (Map<String, Any>?) -> Unit,
    ) {
        if (!sessionId.isNullOrEmpty() && activeSessionId != sessionId) {
            // 若该 sessionId 正在由中断触发的异步 WAV 收尾中，挂载共享其带中断标记的唯一落盘结果。
            val closing = closingWavJob
            if (!deleteFile && closing != null && closing.sessionId == sessionId) {
                closing.markRpcConsumerAttached()
                closing.addCompletionListener { res ->
                    mainHandler.post { onDone(res) }
                }
                return
            }
            onDone(null)
            return
        }
        stopMeterLoop()
        cancelMaxLimitGuard()
        val path = recordingPath
        val started = recordingStartedAt
        val rec = mediaRecorder
        val recCb = mediaRecorderCallback
        val job = wavJob

        mediaRecorder = null
        mediaRecorderCallback = null
        wavJob = null
        recordingPath = null
        recordingStartedAt = 0
        if (!nativeActive) {
            activeSessionId = null
        }

        if (rec == null && job == null) {
            onDone(null)
            return
        }

        val durationMs = maxOf(0L, System.currentTimeMillis() - started).toInt()
        if (rec != null) {
            var stopOk = true
            try {
                rec.stop()
            } catch (_: Exception) {
                stopOk = false
            }
            releaseMediaRecorder(rec, recCb)
            if (deleteFile || !stopOk || path == null || !isNonEmptyFile(path, minBytes = 1)) {
                if (path != null) File(path).delete()
                onDone(null)
                return
            }
            onDone(mapOf("path" to path, "durationMs" to durationMs))
            return
        }

        // WAV 录音先标记 closing 并立即解除硬件阻塞；仅在确认麦克风已释放后才清空 closingWavJob。
        val activeJob = job!!
        closingWavJob = activeJob
        activeJob.addCompletionListener { res ->
            mainHandler.post { onDone(res) }
        }
        activeJob.requestStop()
        Thread {
            val ok = activeJob.awaitFinished(3000)
            val valid = !deleteFile && ok && path != null && isNonEmptyFile(path, minBytes = 45)
            if (!valid && path != null) {
                File(path).delete()
            }
            val res = if (valid && path != null) {
                buildMap<String, Any> {
                    put("path", path)
                    put("durationMs", durationMs)
                    if (interruptedReason != null) {
                        put("interrupted", true)
                        put("reason", interruptedReason)
                    }
                }
            } else {
                null
            }
            activeJob.complete(res)
            mainHandler.post {
                if (closingWavJob === activeJob && activeJob.isMicReleased()) {
                    closingWavJob = null
                }
            }
        }.start()
    }

    private fun isNonEmptyFile(path: String, minBytes: Long): Boolean {
        val f = File(path)
        return f.exists() && f.length() >= minBytes
    }

    private fun releaseMediaRecorder(
        rec: MediaRecorder?,
        cb: AudioManager.AudioRecordingCallback? = null,
    ) {
        if (rec == null) return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q && cb != null) {
            try {
                rec.unregisterAudioRecordingCallback(cb)
            } catch (_: Exception) {
            }
        }
        try {
            rec.reset()
        } catch (_: Exception) {
        }
        try {
            rec.release()
        } catch (_: Exception) {
        }
    }

    private fun scheduleMaxLimitGuard(sessionId: String) {
        cancelMaxLimitGuard()
        val task = Runnable {
            if (activeSessionId == sessionId) {
                interruptActiveCapture(reason = "limit_reached")
            }
        }
        maxLimitRunnable = task
        mainHandler.postDelayed(task, 185_000L)
    }

    private fun cancelMaxLimitGuard() {
        maxLimitRunnable?.let { mainHandler.removeCallbacks(it) }
        maxLimitRunnable = null
    }

    private fun startMeterLoop(sessionId: String) {
        stopMeterLoop()
        val task = object : Runnable {
            override fun run() {
                if (activeSessionId != sessionId) return
                val rec = mediaRecorder ?: return
                val amp = try {
                    rec.maxAmplitude
                } catch (_: Exception) {
                    0
                }
                val norm = if (amp <= 0) {
                    0.0
                } else {
                    val db = 20.0 * log10(amp.toDouble() / 32767.0)
                    normalizeDb(db)
                }
                emit(mapOf("type" to "level", "sessionId" to sessionId, "level" to norm))
                mainHandler.postDelayed(this, 60)
            }
        }
        meterRunnable = task
        mainHandler.postDelayed(task, 60)
    }

    private fun stopMeterLoop() {
        meterRunnable?.let { mainHandler.removeCallbacks(it) }
        meterRunnable = null
    }

    // MARK: - Native Speech Recognition

    private fun startNativeRecognition(
        sessionId: String,
        locale: String?,
        hints: List<String>,
        result: MethodChannel.Result,
    ) {
        preemptActiveCapture()

        val ctx = activity.applicationContext
        if (!isOnDeviceSpeechSupported(ctx)) {
            result.error("speech_unavailable", "on-device speech recognizer unavailable", null)
            return
        }

        runAfterMicReleased(sessionId, result) {
            try {
                val sr = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                    SpeechRecognizer.createOnDeviceSpeechRecognizer(ctx)
                } else {
                    result.error("speech_unavailable", "on-device speech recognizer unavailable", null)
                    return@runAfterMicReleased
                }
                activeSessionId = sessionId
                latestTranscript = ""
                nativeActive = true
                nativeFinishing = false
                speechRecognizer = sr
                sr.setRecognitionListener(
                    object : RecognitionListener {
                        override fun onReadyForSpeech(params: Bundle?) {}
                        override fun onBeginningOfSpeech() {}
                        override fun onRmsChanged(rmsdB: Float) {
                            if (speechRecognizer !== sr || activeSessionId != sessionId) return
                            val norm = ((rmsdB + 2.0f) / 12.0f).coerceIn(0.0f, 1.0f).toDouble()
                            emit(mapOf("type" to "level", "sessionId" to sessionId, "level" to norm))
                        }
                        override fun onBufferReceived(buffer: ByteArray?) {}
                        override fun onEndOfSpeech() {}
                        override fun onError(error: Int) {
                            if (speechRecognizer !== sr || activeSessionId != sessionId) return
                            val wasActive = nativeActive
                            val finishing = nativeFinishing
                            val fallback = latestTranscript
                            cleanupNative()
                            if (wasActive) {
                                if (fallback.isNotEmpty() &&
                                    (finishing ||
                                        error == SpeechRecognizer.ERROR_NO_MATCH ||
                                        error == SpeechRecognizer.ERROR_SPEECH_TIMEOUT)
                                ) {
                                    emit(mapOf("type" to "final", "sessionId" to sessionId, "text" to fallback))
                                } else {
                                    emit(
                                        mapOf(
                                            "type" to "error",
                                            "sessionId" to sessionId,
                                            "code" to "speech_failed",
                                            "message" to "speech error $error",
                                        ),
                                    )
                                }
                            }
                        }
                        override fun onResults(results: Bundle?) {
                            if (speechRecognizer !== sr || activeSessionId != sessionId) return
                            val text = results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)
                                ?.firstOrNull() ?: latestTranscript
                            latestTranscript = text
                            cleanupNative()
                            emit(mapOf("type" to "final", "sessionId" to sessionId, "text" to text))
                        }
                        override fun onPartialResults(partialResults: Bundle?) {
                            if (speechRecognizer !== sr || activeSessionId != sessionId) return
                            val text = partialResults?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)
                                ?.firstOrNull() ?: return
                            latestTranscript = text
                            emit(mapOf("type" to "partial", "sessionId" to sessionId, "text" to text))
                        }
                        override fun onEvent(eventType: Int, params: Bundle?) {}
                    },
                )
                val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
                    putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
                    putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
                    putExtra(RecognizerIntent.EXTRA_PREFER_OFFLINE, true)
                    if (!locale.isNullOrEmpty()) {
                        putExtra(RecognizerIntent.EXTRA_LANGUAGE, locale)
                    }
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && hints.isNotEmpty()) {
                        putStringArrayListExtra(
                            RecognizerIntent.EXTRA_BIASING_STRINGS,
                            ArrayList(hints.take(100)),
                        )
                    }
                }
                sr.startListening(intent)
                result.success(true)
            } catch (e: Exception) {
                cleanupNative()
                result.error("speech_failed", e.message ?: "failed to start speech recognizer", null)
            }
        }
    }

    private fun stopNativeRecognition(sessionId: String?, cancel: Boolean): String? {
        if (!sessionId.isNullOrEmpty() && activeSessionId != sessionId) {
            return null
        }
        if (!nativeActive) return null
        val sid = activeSessionId ?: ""
        val sr = speechRecognizer
        val current = latestTranscript
        // MethodChannel 与原生回调都在主线程执行，同步操作对应识别器实例避免排队闭包误伤后续新会话。
        if (cancel) {
            try {
                sr?.cancel()
            } catch (_: Exception) {
            }
            cleanupNative(expected = sr)
            return null
        }
        nativeFinishing = true
        try {
            sr?.stopListening()
            val fallback = Runnable {
                if (nativeActive && activeSessionId == sid && speechRecognizer === sr) {
                    val text = latestTranscript
                    cleanupNative(expected = sr)
                    emit(mapOf("type" to "final", "sessionId" to sid, "text" to text))
                }
            }
            nativeFinishFallback = fallback
            mainHandler.postDelayed(fallback, 1200L)
        } catch (_: Exception) {
            val text = latestTranscript
            cleanupNative(expected = sr)
            emit(mapOf("type" to "final", "sessionId" to sid, "text" to text))
        }
        return current
    }

    private fun cleanupNative(expected: SpeechRecognizer? = null) {
        if (expected != null && speechRecognizer !== expected) return
        nativeFinishFallback?.let { mainHandler.removeCallbacks(it) }
        nativeFinishFallback = null
        nativeActive = false
        nativeFinishing = false
        try {
            speechRecognizer?.destroy()
        } catch (_: Exception) {
        }
        speechRecognizer = null
        if (mediaRecorder == null && wavJob == null) {
            activeSessionId = null
        }
    }

    companion object {
        internal fun normalizeDb(db: Double): Double {
            if (!db.isFinite() || db <= -55.0) return 0.0
            if (db >= -5.0) return 1.0
            return (db + 55.0) / 50.0
        }
    }
}

/// 单次 WAV 录音任务：持有独立的停止标志、线程与完成状态，避免并发复录互相覆盖。
private class WavRecordingJob(
    val sessionId: String,
    private val outFile: File,
    private val audioRecord: AudioRecord,
    private val sampleRate: Int,
    private val bufSize: Int,
    private val mainHandler: Handler,
    private val onLevel: (Double) -> Unit,
    private val onSilenced: () -> Unit,
    private val onError: (String) -> Unit,
) {
    private val running = AtomicBoolean(true)
    private val micReleased = CountDownLatch(1)
    private val recordLock = Any()
    private var released = false
    private var recordingCallback: AudioManager.AudioRecordingCallback? = null
    private val completionLock = Any()
    private var completed = false
    private var rpcConsumerAttached = false
    private var completionResult: Map<String, Any>? = null
    private val completionListeners = mutableListOf<(Map<String, Any>?) -> Unit>()
    @Volatile
    private var succeeded = false
    private var worker: Thread? = null

    fun isMicReleased(): Boolean = micReleased.count == 0L

    fun markRpcConsumerAttached() {
        synchronized(completionLock) {
            rpcConsumerAttached = true
        }
    }

    fun hasRpcConsumer(): Boolean = synchronized(completionLock) { rpcConsumerAttached }

    fun addCompletionListener(listener: (Map<String, Any>?) -> Unit) {
        var immediate: Map<String, Any>? = null
        var alreadyDone = false
        synchronized(completionLock) {
            if (completed) {
                alreadyDone = true
                immediate = completionResult
            } else {
                completionListeners.add(listener)
            }
        }
        if (alreadyDone) listener(immediate)
    }

    fun complete(result: Map<String, Any>?) {
        val listeners: List<(Map<String, Any>?) -> Unit>
        synchronized(completionLock) {
            if (completed) return
            completed = true
            completionResult = result
            listeners = completionListeners.toList()
            completionListeners.clear()
        }
        listeners.forEach { it(result) }
    }

    private fun releaseMicOnce() {
        synchronized(recordLock) {
            if (released) return
            released = true
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                recordingCallback?.let { cb ->
                    try {
                        audioRecord.unregisterAudioRecordingCallback(cb)
                    } catch (_: Exception) {
                    }
                }
                recordingCallback = null
            }
            try {
                audioRecord.stop()
            } catch (_: Exception) {
            }
            try {
                audioRecord.release()
            } catch (_: Exception) {
            }
            micReleased.countDown()
        }
    }

    fun start() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val cb = object : AudioManager.AudioRecordingCallback() {
                override fun onRecordingConfigChanged(configs: MutableList<AudioRecordingConfiguration>?) {
                    if (configs?.any { it.isClientSilenced } == true) {
                        onSilenced()
                    }
                }
            }
            recordingCallback = cb
            try {
                audioRecord.registerAudioRecordingCallback({ r -> mainHandler.post(r) }, cb)
            } catch (_: Exception) {
                recordingCallback = null
            }
        }
        val thread = Thread {
            var totalBytes = 0L
            var lastMeterAt = 0L
            var readFailed = false
            val buf = ByteArray(bufSize)
            try {
                RandomAccessFile(outFile, "rw").use { raf ->
                    raf.setLength(0)
                    raf.write(ByteArray(44))
                    audioRecord.startRecording()
                    while (running.get()) {
                        val n = audioRecord.read(buf, 0, buf.size)
                        if (n > 0) {
                            raf.write(buf, 0, n)
                            totalBytes += n
                            val now = System.currentTimeMillis()
                            if (now - lastMeterAt >= 60) {
                                lastMeterAt = now
                                onLevel(pcmLevel(buf, n))
                            }
                        } else if (n < 0) {
                            if (running.get()) {
                                readFailed = true
                            }
                            break
                        }
                    }
                    // 先释放硬件麦克风，再完成 WAV 头回填与落盘同步，不阻塞下一笔录音。
                    releaseMicOnce()
                    if (!readFailed && totalBytes > 0) {
                        writeWavHeader(raf, totalBytes, sampleRate, 1, 16)
                        raf.fd.sync()
                        succeeded = true
                    }
                }
            } catch (e: Exception) {
                readFailed = true
                onError(e.message ?: "wav write failed")
            } finally {
                releaseMicOnce()
                if (!succeeded) {
                    outFile.delete()
                }
            }
        }
        worker = thread
        thread.start()
    }

    fun requestStop() {
        running.set(false)
        // 主动调用 stop 解除 audioRecord.read() 的底层阻塞。
        synchronized(recordLock) {
            if (!released) {
                try {
                    audioRecord.stop()
                } catch (_: Exception) {
                }
            }
        }
    }

    fun awaitMicReleased(timeoutMs: Long): Boolean {
        return try {
            micReleased.await(timeoutMs, TimeUnit.MILLISECONDS)
        } catch (_: Exception) {
            false
        }
    }

    fun awaitFinished(timeoutMs: Long): Boolean {
        try {
            worker?.join(timeoutMs)
        } catch (_: Exception) {
            return false
        }
        val alive = worker?.isAlive == true
        return !alive && succeeded
    }

    private fun pcmLevel(buf: ByteArray, len: Int): Double {
        val samples = len / 2
        if (samples <= 0) return 0.0
        var sum = 0.0
        var i = 0
        while (i + 1 < len) {
            val lo = buf[i].toInt() and 0xff
            val hi = buf[i + 1].toInt()
            val sample = ((hi shl 8) or lo).toShort().toDouble() / 32768.0
            sum += sample * sample
            i += 2
        }
        val rms = sqrt(sum / samples)
        val db = if (rms > 0) 20.0 * log10(rms) else -60.0
        return VoicePlugin.normalizeDb(db)
    }

    private fun writeWavHeader(
        raf: RandomAccessFile,
        pcmBytes: Long,
        sampleRate: Int,
        channels: Int,
        bitsPerSample: Int,
    ) {
        val byteRate = sampleRate * channels * bitsPerSample / 8
        val blockAlign = channels * bitsPerSample / 8
        val chunkSize = 36 + pcmBytes
        raf.seek(0)
        val header = ByteArray(44)
        fun putAscii(off: Int, s: String) {
            for (i in s.indices) header[off + i] = s[i].code.toByte()
        }
        fun putInt(off: Int, v: Int) {
            header[off] = (v and 0xff).toByte()
            header[off + 1] = ((v ushr 8) and 0xff).toByte()
            header[off + 2] = ((v ushr 16) and 0xff).toByte()
            header[off + 3] = ((v ushr 24) and 0xff).toByte()
        }
        fun putShort(off: Int, v: Int) {
            header[off] = (v and 0xff).toByte()
            header[off + 1] = ((v ushr 8) and 0xff).toByte()
        }
        putAscii(0, "RIFF")
        putInt(4, chunkSize.toInt())
        putAscii(8, "WAVE")
        putAscii(12, "fmt ")
        putInt(16, 16)
        putShort(20, 1)
        putShort(22, channels)
        putInt(24, sampleRate)
        putInt(28, byteRate)
        putShort(32, blockAlign)
        putShort(34, bitsPerSample)
        putAscii(36, "data")
        putInt(40, pcmBytes.toInt())
        raf.write(header)
    }
}

/// 语音 API Key 安全存储（posse/voice/secret）：Keystore 不可导出 AES-256-GCM 密钥 + noBackupFilesDir。
private class VoiceSecretStore(context: Context, messenger: BinaryMessenger) {
    private val file = File(context.noBackupFilesDir, "voice-secrets.json")
    private val alias = "posse-voice-secret"
    private val channel = MethodChannel(messenger, "posse/voice/secret")

    init {
        channel.setMethodCallHandler { call, result ->
            try {
                handle(call, result)
            } catch (e: Exception) {
                result.error("failed", e.message ?: "secret store error", null)
            }
        }
    }

    fun dispose() {
        channel.setMethodCallHandler(null)
    }

    private fun handle(call: MethodCall, result: MethodChannel.Result) {
        if (call.method == "clearAll") {
            file.delete()
            result.success(null)
            return
        }
        val key = call.argument<String>("key")
        if (key.isNullOrEmpty()) {
            result.error("bad_args", "missing key", null)
            return
        }
        when (call.method) {
            "has" -> result.success(load().has(key))
            "read" -> result.success(read(key))
            "save" -> result.success(save(key, call.argument<String>("value") ?: ""))
            "delete" -> {
                val all = load()
                all.remove(key)
                result.success(store(all))
            }
            else -> result.notImplemented()
        }
    }

    private fun load(): JSONObject = try {
        JSONObject(file.readText())
    } catch (_: Exception) {
        JSONObject()
    }

    private fun store(all: JSONObject): Boolean {
        return try {
            val tmp = File(file.path + ".tmp")
            tmp.writeText(all.toString())
            if (tmp.renameTo(file)) {
                true
            } else {
                // 部分文件系统 renameTo 不能覆盖已有目标：改用复制后清理临时文件。
                file.writeText(all.toString())
                tmp.delete()
                true
            }
        } catch (_: Exception) {
            false
        }
    }

    private fun secret(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(alias, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .build(),
        )
        return gen.generateKey()
    }

    private fun read(key: String): String? {
        val raw = load().optString(key, "")
        if (raw.isEmpty()) return null
        return try {
            val bytes = Base64.decode(raw, Base64.NO_WRAP)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, secret(), GCMParameterSpec(128, bytes, 0, 12))
            String(cipher.doFinal(bytes, 12, bytes.size - 12), Charsets.UTF_8)
        } catch (_: Exception) {
            null
        }
    }

    private fun save(key: String, value: String): Boolean {
        if (value.isEmpty()) return false
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, secret())
        val sealed = cipher.iv + cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        val all = load()
        all.put(key, Base64.encodeToString(sealed, Base64.NO_WRAP))
        return store(all)
    }
}
