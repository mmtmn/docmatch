package br.com.docmatch.app.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.Shapes
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

/** Colors outside Material's scheme (severity, confidence), from the same tokens. */
data class DmExtraColors(
    val severityInfo: Color,
    val severityLow: Color,
    val severityMedium: Color,
    val severityHigh: Color,
    val severityCritical: Color,
    val onSeverity: Color,
    val confirmed: Color,
    val probable: Color,
    val needsYou: Color,
    val onConfidence: Color,
    val success: Color,
    val muted: Color,
)

val LocalDmColors = staticCompositionLocalOf { lightExtras() }

private fun lightScheme(): ColorScheme =
    with(DmColors.Light) {
        lightColorScheme(
            primary = brandPrimary,
            onPrimary = brandOnPrimary,
            primaryContainer = brandSubtle,
            onPrimaryContainer = brandOnSubtle,
            secondary = brandPrimary,
            onSecondary = brandOnPrimary,
            secondaryContainer = brandSubtle,
            onSecondaryContainer = brandOnSubtle,
            tertiary = textSecondary,
            onTertiary = bgCanvas,
            tertiaryContainer = bgSubtle,
            onTertiaryContainer = textPrimary,
            background = bgCanvas,
            onBackground = textPrimary,
            surface = bgCanvas,
            onSurface = textPrimary,
            surfaceVariant = bgSurface,
            onSurfaceVariant = textSecondary,
            surfaceContainer = bgSurface,
            surfaceContainerLowest = bgCanvas,
            surfaceContainerLow = bgSurface,
            surfaceContainerHigh = bgSurface,
            surfaceContainerHighest = bgSubtle,
            surfaceBright = bgElevated,
            surfaceDim = bgSubtle,
            surfaceTint = bgSurface,
            inverseSurface = bgInverse,
            inverseOnSurface = textInverse,
            inversePrimary = brandPrimaryHover,
            outline = borderStrong,
            outlineVariant = borderDefault,
            error = statusDanger,
            onError = statusOnDanger,
            errorContainer = statusDangerSoft,
            onErrorContainer = statusOnDangerSoft,
        )
    }

private fun darkScheme(): ColorScheme =
    with(DmColors.Dark) {
        darkColorScheme(
            primary = brandPrimary,
            onPrimary = brandOnPrimary,
            primaryContainer = brandSubtle,
            onPrimaryContainer = brandOnSubtle,
            secondary = brandPrimary,
            onSecondary = brandOnPrimary,
            secondaryContainer = brandSubtle,
            onSecondaryContainer = brandOnSubtle,
            tertiary = textSecondary,
            onTertiary = bgCanvas,
            tertiaryContainer = bgSubtle,
            onTertiaryContainer = textPrimary,
            background = bgCanvas,
            onBackground = textPrimary,
            surface = bgCanvas,
            onSurface = textPrimary,
            surfaceVariant = bgSurface,
            onSurfaceVariant = textSecondary,
            surfaceContainer = bgSurface,
            surfaceContainerLowest = bgCanvas,
            surfaceContainerLow = bgSurface,
            surfaceContainerHigh = bgSurface,
            surfaceContainerHighest = bgSubtle,
            surfaceBright = bgElevated,
            surfaceDim = bgSubtle,
            surfaceTint = bgSurface,
            inverseSurface = bgInverse,
            inverseOnSurface = textInverse,
            inversePrimary = brandPrimaryHover,
            outline = borderStrong,
            outlineVariant = borderDefault,
            error = statusDanger,
            onError = statusOnDanger,
            errorContainer = statusDangerSoft,
            onErrorContainer = statusOnDangerSoft,
        )
    }

private fun lightExtras() =
    with(DmColors.Light) {
        DmExtraColors(
            severityInfo, severityLow, severityMedium, severityHigh, severityCritical, severityOnInfo,
            confidenceConfirmed, confidenceProbable, confidenceNeedsYou, confidenceOnConfirmed, statusSuccess, textMuted,
        )
    }

private fun darkExtras() =
    with(DmColors.Dark) {
        DmExtraColors(
            severityInfo, severityLow, severityMedium, severityHigh, severityCritical, severityOnInfo,
            confidenceConfirmed, confidenceProbable, confidenceNeedsYou, confidenceOnConfirmed, statusSuccess, textMuted,
        )
    }

@Composable
fun DocmatchTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalDmColors provides if (dark) darkExtras() else lightExtras()) {
        MaterialTheme(colorScheme = if (dark) darkScheme() else lightScheme(),
            typography = Typography(
                headlineLarge = TextStyle(fontSize = 30.sp, lineHeight = 35.sp, fontWeight = FontWeight.Bold, letterSpacing = (-0.7).sp),
                headlineSmall = TextStyle(fontSize = 24.sp, lineHeight = 30.sp, fontWeight = FontWeight.Bold, letterSpacing = (-0.4).sp),
                titleLarge = TextStyle(fontSize = 21.sp, lineHeight = 28.sp, fontWeight = FontWeight.SemiBold),
                titleMedium = TextStyle(fontSize = 17.sp, lineHeight = 24.sp, fontWeight = FontWeight.SemiBold),
                titleSmall = TextStyle(fontSize = 15.sp, lineHeight = 22.sp, fontWeight = FontWeight.SemiBold),
                bodyLarge = TextStyle(fontSize = 16.sp, lineHeight = 25.sp),
                bodyMedium = TextStyle(fontSize = 15.sp, lineHeight = 23.sp),
                bodySmall = TextStyle(fontSize = 13.sp, lineHeight = 20.sp),
                labelLarge = TextStyle(fontSize = 15.sp, lineHeight = 21.sp, fontWeight = FontWeight.SemiBold),
            ),
            shapes = Shapes(small = RoundedCornerShape(DmSize.radiusMd), medium = RoundedCornerShape(DmSize.radiusLg), large = RoundedCornerShape(DmSize.radiusLg)),
            content = content)
    }
}
