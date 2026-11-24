package com.neolabsapp.posse.mobile

import android.Manifest
import android.content.ContentValues
import android.content.Context
import android.content.pm.PackageManager
import android.media.MediaScannerConnection
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Build
import android.os.Environment
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.File

class MainActivity : FlutterActivity() {
    // 安卓 9 及以下等写存储权限的那次调用
    private var pending: (() -> Unit)? = null
    private var pendingResult: MethodChannel.Result? = null
    // 等相机权限的那次 authorize
    private var cameraResult: MethodChannel.Result? = null
    private var update: AppUpdate? = null
    private var desk: DeskPlugin? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        appContext = applicationContext
        tsInit()
        val messenger = flutterEngine.dartExecutor.binaryMessenger
        flutterEngine.platformViewsController.registry
            .registerViewFactory("posse/qr", QrScanViewFactory(messenger, this))
        val desk = DeskPlugin(this, flutterEngine.renderer, messenger)
        this.desk = desk
        flutterEngine.platformViewsController.registry.registerViewFactory("posse/desk/view", desk.viewFactory)
        update = AppUpdate(this, messenger)
        MethodChannel(messenger, "posse/qr").setMethodCallHandler { call, result ->
            if (call.method != "authorize") {
                result.notImplemented()
                return@setMethodCallHandler
            }
            if (checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                result.success(true)
                return@setMethodCallHandler
            }
            cameraResult?.success(false)
            cameraResult = result
            requestPermissions(arrayOf(Manifest.permission.CAMERA), REQUEST_CAMERA)
        }
        MethodChannel(messenger, "posse/tailscale").setMethodCallHandler { call, result ->
            if (call.method != "stateDir") {
                result.notImplemented()
                return@setMethodCallHandler
            }
            try {
                val dir = File(noBackupFilesDir, "tailscale")
                if (!dir.exists() && !dir.mkdirs()) {
                    result.error("failed", "failed to create tailscale state directory", null)
                    return@setMethodCallHandler
                }
                result.success(dir.absolutePath)
            } catch (e: Exception) {
                result.error("failed", e.message ?: "failed to prepare tailscale state directory", null)
            }
        }
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "posse/downloads")
            .setMethodCallHandler { call, result ->
                if (call.method != "save") {
                    result.notImplemented()
                    return@setMethodCallHandler
                }
                val path = call.argument<String>("path")!!
                val name = call.argument<String>("name")!!
                val run = { saveInBackground(File(path), name, result) }
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ||
                    checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE) ==
                    PackageManager.PERMISSION_GRANTED
                ) {
                    run()
                } else {
                    pending = run
                    pendingResult = result
                    requestPermissions(arrayOf(Manifest.permission.WRITE_EXTERNAL_STORAGE), REQUEST_WRITE)
                }
            }
    }

    // 首页按返回只退到后台、不关 Activity，和 iOS 一样连接留着。安卓 16 的预测性返回不经过这里，
    // Activity 照样被关掉，靠下面 onDestroy 断开连接
    override fun popSystemNavigator(): Boolean {
        moveTaskToBack(true)
        return true
    }

    // Activity 关掉时 Flutter 引擎跟着销毁、进程还在：先让 Go 断开 QUIC 连接、不再回调，
    // 否则之后的连接事件调到失效的 Dart 回调上，进程崩溃（小米上进程被冻住，回到前台解冻时必现）
    override fun onDestroy() {
        quicReset()
        desk?.dispose()
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        update?.resumed()
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQUEST_CAMERA) {
            cameraResult?.success(grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED)
            cameraResult = null
            return
        }
        if (requestCode != REQUEST_WRITE) return
        val run = pending
        val result = pendingResult
        pending = null
        pendingResult = null
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) {
            run?.invoke()
        } else {
            result?.error("denied", "storage permission denied", null)
        }
    }

    // 复制可能有几百 MB，不放在主线程；回复要回到主线程。
    private fun saveInBackground(src: File, name: String, result: MethodChannel.Result) {
        Thread {
            try {
                val saved = save(src, name)
                runOnUiThread { result.success(saved) }
            } catch (e: Exception) {
                runOnUiThread { result.error("failed", e.message, null) }
            }
        }.start()
    }

    // 存进"下载/Posse"，返回实际的文件名（重名时系统或这里会改成 "x (1).md"）。
    private fun save(src: File, name: String): String {
        val ext = name.substringAfterLast('.', "").lowercase()
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.RELATIVE_PATH, "${Environment.DIRECTORY_DOWNLOADS}/Posse")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: throw IllegalStateException("MediaStore insert failed")
            try {
                contentResolver.openOutputStream(uri)!!.use { out -> src.inputStream().use { it.copyTo(out) } }
                contentResolver.update(uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null)
            } catch (e: Exception) {
                contentResolver.delete(uri, null, null)
                throw e
            }
            contentResolver.query(uri, arrayOf(MediaStore.Downloads.DISPLAY_NAME), null, null, null)?.use {
                if (it.moveToFirst()) return it.getString(0)
            }
            return name
        }
        @Suppress("DEPRECATION")
        val dir = File(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "Posse")
        dir.mkdirs()
        val base = name.substringBeforeLast('.')
        val dot = if (ext.isEmpty()) "" else ".$ext"
        var dest = File(dir, name)
        var n = 1
        while (dest.exists()) dest = File(dir, "$base (${n++})$dot")
        src.copyTo(dest)
        MediaScannerConnection.scanFile(this, arrayOf(dest.path), arrayOf(mime), null)
        return dest.name
    }

    companion object {
        init {
            System.loadLibrary("posse_quic")
        }

        @Volatile
        private var appContext: Context? = null

        // 在 packages/posse_quic/go/jni_android.go
        @JvmStatic
        private external fun quicReset()

        // 在 packages/posse_quic/go/jni_android.go：注册底层物理网络绑定回调
        @JvmStatic
        private external fun tsInit()

        // 由 Go (tsnet netns) 通过 JNI 调用：把 socket 绑定到底层非 VPN 物理网络，避免与系统 VPN 环路。
        // 使用 ParcelFileDescriptor.fromFd 拷贝 fd 后绑定，关闭拷贝不影响 Go 持有的原始 fd。
        @JvmStatic
        fun bindSocketToActiveNetwork(fd: Int): Int {
            val ctx = appContext ?: return 0
            val cm = ctx.getSystemService(ConnectivityManager::class.java) ?: return 0
            val net = pickUnderlyingNetwork(cm)
            if (net == null) {
                // 若存在活跃 VPN 但找不到底层物理网络，返回错误避免发包回环进 VPN；否则放行默认路由。
                return if (hasActiveVpn(cm)) -2 else 0
            }
            return try {
                ParcelFileDescriptor.fromFd(fd).use { pfd ->
                    net.bindSocket(pfd.fileDescriptor)
                }
                0
            } catch (_: Exception) {
                -1
            }
        }

        private fun pickUnderlyingNetwork(cm: ConnectivityManager): Network? {
            val active = cm.activeNetwork
            if (active != null && isUnderlyingNetwork(cm, active, requireValidated = true)) {
                return active
            }
            @Suppress("DEPRECATION")
            val all = cm.allNetworks
            for (net in all) {
                if (isUnderlyingNetwork(cm, net, requireValidated = true)) {
                    return net
                }
            }
            if (active != null && isUnderlyingNetwork(cm, active, requireValidated = false)) {
                return active
            }
            for (net in all) {
                if (isUnderlyingNetwork(cm, net, requireValidated = false)) {
                    return net
                }
            }
            return null
        }

        private fun isUnderlyingNetwork(
            cm: ConnectivityManager,
            net: Network,
            requireValidated: Boolean,
        ): Boolean {
            val caps = cm.getNetworkCapabilities(net) ?: return false
            if (!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) return false
            if (!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) return false
            if (requireValidated && !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)) {
                return false
            }
            return true
        }

        private fun hasActiveVpn(cm: ConnectivityManager): Boolean {
            val active = cm.activeNetwork ?: return false
            val caps = cm.getNetworkCapabilities(active) ?: return false
            return caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) ||
                !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        }

        private const val REQUEST_WRITE = 7301
        private const val REQUEST_CAMERA = 7302
    }
}
