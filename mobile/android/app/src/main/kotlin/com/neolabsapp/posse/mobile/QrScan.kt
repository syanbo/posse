package com.neolabsapp.posse.mobile

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Size
import android.view.View
import androidx.camera.core.CameraSelector
import androidx.camera.core.FocusMeteringAction
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.core.SurfaceOrientedMeteringPointFactory
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.LuminanceSource
import com.google.zxing.NotFoundException
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.MethodChannel
import io.flutter.plugin.common.StandardMessageCodec
import io.flutter.plugin.platform.PlatformView
import io.flutter.plugin.platform.PlatformViewFactory
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

class QrScanViewFactory(
    private val messenger: BinaryMessenger,
    private val owner: LifecycleOwner,
) : PlatformViewFactory(StandardMessageCodec.INSTANCE) {
    override fun create(context: Context, viewId: Int, args: Any?): PlatformView =
        QrScanPlatformView(context, viewId, messenger, owner)
}

/// 预览走 PreviewView，识别在分析线程上用 ZXing 读亮度平面；只认二维码，和 iOS 一样只上报文字。
class QrScanPlatformView(
    context: Context,
    viewId: Int,
    messenger: BinaryMessenger,
    private val owner: LifecycleOwner,
) : PlatformView {
    // Flutter 的平台视图合成不了 SurfaceView，要 TextureView
    private val preview = PreviewView(context).apply {
        implementationMode = PreviewView.ImplementationMode.COMPATIBLE
        scaleType = PreviewView.ScaleType.FILL_CENTER
    }
    private val channel = MethodChannel(messenger, "posse/qr/$viewId")
    private val main = Handler(Looper.getMainLooper())
    private val analyzer: ExecutorService = Executors.newSingleThreadExecutor()
    private val reader = QRCodeReader()
    private val hints = mapOf(
        DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE),
        DecodeHintType.TRY_HARDER to true,
    )
    private var provider: ProcessCameraProvider? = null
    private var last: String? = null
    @Volatile private var stopped = false

    init {
        channel.setMethodCallHandler { call, result ->
            if (call.method == "stop") {
                stop()
                result.success(null)
            } else {
                result.notImplemented()
            }
        }
        val future = ProcessCameraProvider.getInstance(context)
        future.addListener({
            try {
                bind(future.get())
            } catch (e: Exception) {
                fail()
            }
        }, ContextCompat.getMainExecutor(context))
    }

    override fun getView(): View = preview

    override fun dispose() {
        stop()
        channel.setMethodCallHandler(null)
        analyzer.shutdown()
    }

    private fun bind(cameras: ProcessCameraProvider) {
        if (stopped) return
        provider = cameras
        val pv = Preview.Builder().build().also { it.surfaceProvider = preview.surfaceProvider }
        // 配对码在眼前的屏幕上，720p 足够认，分析也快
        val analysis = ImageAnalysis.Builder()
            .setResolutionSelector(
                ResolutionSelector.Builder()
                    .setResolutionStrategy(
                        ResolutionStrategy(
                            Size(1280, 720),
                            ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                        ),
                    )
                    .build(),
            )
            .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
            .build()
        analysis.setAnalyzer(analyzer) { image -> analyze(image) }
        try {
            cameras.unbindAll()
            val camera = cameras.bindToLifecycle(owner, CameraSelector.DEFAULT_BACK_CAMERA, pv, analysis)
            // 对准中心连续对焦，近处的屏幕锁得快
            val point = SurfaceOrientedMeteringPointFactory(1f, 1f).createPoint(0.5f, 0.5f)
            camera.cameraControl.startFocusAndMetering(
                FocusMeteringAction.Builder(point, FocusMeteringAction.FLAG_AF).build(),
            )
        } catch (e: Exception) {
            fail()
        }
    }

    private fun analyze(image: ImageProxy) {
        try {
            if (stopped) return
            val text = decode(image) ?: return
            if (text.isEmpty() || text == last) return
            last = text
            main.post { if (!stopped) channel.invokeMethod("code", text) }
        } finally {
            image.close()
        }
    }

    private fun decode(image: ImageProxy): String? {
        val plane = image.planes[0]
        val width = image.width
        val height = image.height
        val stride = plane.rowStride
        val buf = plane.buffer
        val data = ByteArray(stride * height)
        buf.rewind()
        buf.get(data, 0, minOf(data.size, buf.remaining()))
        val source = PlanarYUVLuminanceSource(data, stride, height, 0, 0, width, height, false)
        // 深色主题的配对码是浅色码画在深色底上，正着读不到再反色读一次
        return read(source) ?: read(source.invert())
    }

    private fun read(source: LuminanceSource): String? {
        return try {
            reader.decode(BinaryBitmap(HybridBinarizer(source)), hints).text
        } catch (e: NotFoundException) {
            null
        } catch (e: ReaderException) {
            null
        } finally {
            reader.reset()
        }
    }

    private fun stop() {
        if (stopped) return
        stopped = true
        main.post { provider?.unbindAll() }
    }

    private fun fail() {
        main.post { channel.invokeMethod("failed", null) }
    }
}
