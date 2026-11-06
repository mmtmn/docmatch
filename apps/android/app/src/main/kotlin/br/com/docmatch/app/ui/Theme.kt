package br.com.docmatch.app.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
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
        MaterialTheme(colorScheme = if (dark) darkScheme() else lightScheme(), content = content)
    }
}
