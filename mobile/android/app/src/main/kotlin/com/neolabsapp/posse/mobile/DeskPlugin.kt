package com.neolabsapp.posse.mobile

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import io.flutter.view.TextureRegistry
import org.json.JSONObject
import java.io.File
import java.net.InetSocketAddress
import java.security.KeyStore
import java.util.concurrent.Executors
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/// 看桌面的原生层，通道和参数同 ios/Runner/DeskPlugin.swift。画面不经 Dart：解码器直接画进 Flutter 纹理的 Surface。
class DeskPlugin(
    private val context: Context,
    private val textures: TextureRegistry,
    messenger: BinaryMessenger,
) : EventChannel.StreamHandler {
    private val method = MethodChannel(messenger, "posse/desk")
    private val events = EventChannel(messenger, "posse/desk/stats")
    private val main = Handler(Looper.getMainLooper())
    /// 开始和停止排在这一条线程上：停要等收包线程退出，查 DNS 也不能在主线程
    private val control = Executors.newSingleThreadExecutor()
    private val lock = Object()
    private var session: DeskSession? = null
    private var texture: TextureRegistry.SurfaceProducer? = null
    private var generation = 0
    private var sink: EventChannel.EventSink? = null
    /// 平台视图一直挂着时不会再报 surfaceCreated，新开的会话从这里拿
    private var viewSurface: android.view.Surface? = null

    init {
        method.setMethodCallHandler { call, result -> handle(call, result) }
        DeskOrientation(context as android.app.Activity, messenger)
        events.setStreamHandler(this)
        DeskPassword(context, messenger)
    }

    override fun onListen(arguments: Any?, events: EventChannel.EventSink) {
        sink = events
    }

    override fun onCancel(arguments: Any?) {
        sink = null
    }

    private fun current(): DeskSession? = synchronized(lock) { session }

    /// 画面的平台视图，海思解码器用（见 DeskSession.view）。
    val viewFactory = object : io.flutter.plugin.platform.PlatformViewFactory(io.flutter.plugin.common.StandardMessageCodec.INSTANCE) {
        override fun create(context: Context, viewId: Int, args: Any?): io.flutter.plugin.platform.PlatformView {
            val surfaceView = android.view.SurfaceView(context)
            surfaceView.holder.addCallback(object : android.view.SurfaceHolder.Callback {
                override fun surfaceCreated(holder: android.view.SurfaceHolder) {
                    viewSurface = holder.surface
                    current()?.setViewSurface(holder.surface)
                }

                override fun surfaceChanged(holder: android.view.SurfaceHolder, format: Int, width: Int, height: Int) {}

                override fun surfaceDestroyed(holder: android.view.SurfaceHolder) {
                    viewSurface = null
                    current()?.setViewSurface(null)
                }
            })
            return object : io.flutter.plugin.platform.PlatformView {
                override fun getView(): android.view.View = surfaceView
                override fun dispose() {}
            }
        }
    }

    private fun handle(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "start" -> start(call, result)
            "stop" -> stop(result)
            "pause" -> {
                current()?.setPaused(true)
                result.success(null)
            }
            "resume" -> {
                current()?.setPaused(false)
                result.success(null)
            }
            "input" -> {
                val plain = call.arguments as? ByteArray
                if (plain == null) {
                    result.error("bad_args", "missing input", null)
                } else {
                    current()?.sendInput(plain)
                    result.success(null)
                }
            }
            "punch" -> {
                val text = call.arguments as? String
                if (text == null) {
                    result.error("bad_address", "bad address", null)
                    return
                }
                control.execute {
                    val peer = deskResolveHostPort(text)
                    main.post {
                        if (peer == null) {
                            result.error("bad_address", "bad address", null)
                        } else {
                            current()?.punch(peer)
                            result.success(null)
                        }
                    }
                }
            }
            else -> result.notImplemented()
        }
    }

    private fun start(call: MethodCall, result: MethodChannel.Result) {
        val toPhone = call.argument<ByteArray>("keyToPhone")
        val toMac = call.argument<ByteArray>("keyToMac")
        val codec = call.argument<String>("codec")
        if (toPhone == null || toMac == null || codec == null) {
            result.error("bad_args", "bad host, port or key", null)
            return
        }
        if (toPhone.size != 32 || toMac.size != 32) {
            result.error("bad_args", "bad key length", null)
            return
        }
        val decoderName = deskHevcDecoder()
        if (codec != "hevc" || decoderName == null) {
            result.error("unsupported_codec", "codec not supported yet", null)
            return
        }
        val host = call.argument<String>("host")
        val port = call.argument<Number>("port")?.toInt() ?: 0
        val relayText = call.argument<String>("relay")
        val relayToken = call.argument<ByteArray>("relayToken")
        val nack = call.argument<Boolean>("nack") ?: false
        control.execute {
            val gen = stopSession()
            // 直连地址可以没有（经隧道连着、只能走中转），中转也可以没有，但不能都没有
            val peers = ArrayList<InetSocketAddress>()
            if (!host.isNullOrEmpty()) {
                val peer = deskResolve(host, port)
                if (peer == null) {
                    main.post { result.error("bad_address", "cannot resolve $host", null) }
                    return@execute
                }
                peers.add(peer)
            }
            val relay = if (relayText.isNullOrEmpty()) null else deskResolveHostPort(relayText)
            if (peers.isEmpty() && relay == null) {
                main.post { result.error("bad_address", "no address to connect to", null) }
                return@execute
            }
            val view = decoderName.startsWith("OMX.hisi") || decoderName.startsWith("c2.hisi")
            main.post { open(gen, toPhone, toMac, peers, relay, relayToken, nack, view, result) }
        }
    }

    private fun open(
        gen: Int,
        toPhone: ByteArray,
        toMac: ByteArray,
        peers: List<InetSocketAddress>,
        relay: InetSocketAddress?,
        relayToken: ByteArray?,
        nack: Boolean,
        view: Boolean,
        result: MethodChannel.Result,
    ) {
        if (synchronized(lock) { generation != gen }) {
            result.error("cancelled", "connection cancelled", null)
            return
        }
        val created = try {
            DeskSession(toPhone, toMac, peers, relay, relayToken, nack, view)
        } catch (e: Exception) {
            result.error("desk", "cannot open the receiver", null)
            return
        }
        val producer = textures.createSurfaceProducer()
        created.onStats = { stats -> main.post { sink?.success(stats) } }
        // App 退到后台时纹理的 Surface 会被收回，回来再给一个新的；解码器跟着重建
        producer.setCallback(object : TextureRegistry.SurfaceProducer.Callback {
            override fun onSurfaceAvailable() {
                created.setTextureSurface(producer.surface)
        viewSurface?.let { created.setViewSurface(it) }
            }

            override fun onSurfaceCleanup() {
                created.setTextureSurface(null)
            }
        })
        created.setTextureSurface(producer.surface)
        synchronized(lock) {
            if (generation != gen) {
                producer.release()
                result.error("cancelled", "connection cancelled", null)
                return
            }
            session = created
        }
        texture = producer
        created.start()
        result.success(producer.id())
    }

    /// Activity 关掉时停掉接收：Dart 那边等不到退订的回复，不会再来调 stop，收包线程会一直让 Mac 推流。
    fun dispose() {
        control.execute { stopSession() }
    }

    private fun stop(result: MethodChannel.Result) {
        control.execute {
            stopSession()
            main.post {
                texture?.release()
                texture = null
                result.success(null)
            }
        }
    }

    /// 在 control 线程上停掉收包线程。返回的代号用来认出这次停掉之后才开始的连接。
    private fun stopSession(): Int {
        val old: DeskSession?
        val token: Int
        synchronized(lock) {
            generation++
            token = generation
            old = session
            session = null
        }
        if (old != null) {
            old.onStats = null
            old.requestStop()
            old.waitUntilStopped()
        }
        return token
    }
}

/// 看桌面记住的 Mac 登录密码，按电脑（主机公钥）各存一条。密文放在不进备份的目录里，
/// 密钥在 Android Keystore、导不出这台手机。取的时候不另外验证：能打开 Posse 的人本来就能操作这台 Mac 的终端。
private class DeskPassword(context: Context, messenger: BinaryMessenger) {
    private val file = File(context.noBackupFilesDir, "desk-passwords.json")
    private val alias = "posse-desk-password"

    init {
        MethodChannel(messenger, "posse/desk/password").setMethodCallHandler { call, result ->
            try {
                handle(call, result)
            } catch (e: Exception) {
                result.error("failed", e.message, null)
            }
        }
    }

    private fun handle(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "count" -> {
                result.success(load().length())
                return
            }
            "forgetAll" -> {
                file.delete()
                result.success(null)
                return
            }
        }
        val key = call.argument<String>("key")
        if (key.isNullOrEmpty()) {
            result.error("bad_args", "missing key", null)
            return
        }
        when (call.method) {
            "has" -> result.success(load().has(key))
            "read" -> result.success(read(key))
            "save" -> result.success(save(key, call.argument<String>("password") ?: ""))
            "forget" -> {
                val all = load()
                all.remove(key)
                store(all)
                result.success(null)
            }
            else -> result.notImplemented()
        }
    }

    private fun load(): JSONObject = try {
        JSONObject(file.readText())
    } catch (e: Exception) {
        JSONObject()
    }

    private fun store(all: JSONObject) {
        val tmp = File(file.path + ".tmp")
        tmp.writeText(all.toString())
        tmp.renameTo(file)
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
        } catch (e: Exception) {
            null
        }
    }

    private fun save(key: String, password: String): Boolean {
        if (password.isEmpty()) return false
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, secret())
        val sealed = cipher.iv + cipher.doFinal(password.toByteArray(Charsets.UTF_8))
        val all = load()
        all.put(key, Base64.encodeToString(sealed, Base64.NO_WRAP))
        store(all)
        return true
    }
}

/// 看桌面锁方向、离开时放开。用 SENSOR_LANDSCAPE 而不是 SystemChrome 给的 USER_LANDSCAPE：
/// 用户点了横屏就转，不受自动旋转开关的限制。
private class DeskOrientation(activity: android.app.Activity, messenger: BinaryMessenger) {
    init {
        MethodChannel(messenger, "posse/desk/orientation").setMethodCallHandler { call, result ->
            val value = when (call.method) {
                "lock" -> if (call.arguments as? Boolean == true) {
                    android.content.pm.ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE
                } else {
                    android.content.pm.ActivityInfo.SCREEN_ORIENTATION_PORTRAIT
                }
                "release" -> android.content.pm.ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
                else -> {
                    result.notImplemented()
                    return@setMethodCallHandler
                }
            }
            activity.requestedOrientation = value
            result.success(null)
        }
    }
}
