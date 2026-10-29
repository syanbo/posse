package com.dengbo.posse.mobile

import android.Manifest
import android.content.ContentValues
import android.content.pm.PackageManager
import android.media.MediaScannerConnection
import android.os.Build
import android.os.Environment
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

        // 在 packages/posse_quic/go/jni_android.go
        @JvmStatic
        private external fun quicReset()

        private const val REQUEST_WRITE = 7301
        private const val REQUEST_CAMERA = 7302
    }
}
