package br.com.docmatch.app.ui

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.PathBuilder
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.unit.dp

/** Small outline icons matching the site's stroke language; no icon-font dependency. */
object DmIcons {
    private fun icon(name: String, draw: PathBuilder.() -> Unit): ImageVector =
        ImageVector.Builder(name, 24.dp, 24.dp, 24f, 24f).apply {
            path(stroke = SolidColor(Color.Black), strokeLineWidth = 1.8f, strokeLineCap = StrokeCap.Round,
                strokeLineJoin = StrokeJoin.Round, pathBuilder = draw)
        }.build()
    val File = icon("Document") {
        moveTo(14f, 3f); lineTo(6f, 3f); lineTo(6f, 21f); lineTo(19f, 21f); lineTo(19f, 8f); lineTo(14f, 3f)
        moveTo(14f, 3f); lineTo(14f, 8f); lineTo(19f, 8f)
        moveTo(9f, 12f); lineTo(16f, 12f); moveTo(9f, 16f); lineTo(14f, 16f)
    }
    val Upload = icon("Upload") {
        moveTo(12f, 16f); lineTo(12f, 3f); moveTo(7f, 8f); lineTo(12f, 3f); lineTo(17f, 8f)
        moveTo(4f, 15f); lineTo(4f, 21f); lineTo(20f, 21f); lineTo(20f, 15f)
    }
    val Check = icon("Verified document") {
        moveTo(14f, 3f); lineTo(5f, 3f); lineTo(5f, 21f); lineTo(19f, 21f); lineTo(19f, 8f); lineTo(14f, 3f)
        moveTo(14f, 3f); lineTo(14f, 8f); lineTo(19f, 8f)
        moveTo(8f, 14f); lineTo(11f, 17f); lineTo(16f, 12f)
    }
    val Folder = icon("Folder") {
        moveTo(3f, 7f); lineTo(3f, 20f); lineTo(21f, 20f); lineTo(21f, 7f); lineTo(12f, 7f)
        lineTo(10f, 4f); lineTo(3f, 4f); lineTo(3f, 7f)
    }
    private val eye: PathBuilder.() -> Unit = {
        moveTo(2f, 12f); curveTo(7f, 3f, 17f, 3f, 22f, 12f); curveTo(17f, 21f, 7f, 21f, 2f, 12f)
        moveTo(15f, 12f); curveTo(15f, 8f, 9f, 8f, 9f, 12f); curveTo(9f, 16f, 15f, 16f, 15f, 12f)
    }
    val Eye = icon("Show password", eye)
    val EyeOff = icon("Hide password") { eye(); moveTo(3f, 3f); lineTo(21f, 21f) }
    val Back = icon("Back") { moveTo(15f, 5f); lineTo(8f, 12f); lineTo(15f, 19f) }
    val Down = icon("Expand") { moveTo(6f, 9f); lineTo(12f, 15f); lineTo(18f, 9f) }
    val Up = icon("Collapse") { moveTo(6f, 15f); lineTo(12f, 9f); lineTo(18f, 15f) }
}
