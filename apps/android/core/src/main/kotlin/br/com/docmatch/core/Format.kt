// Money, dates and sizes the pt-BR way, identical on every screen of the app.
package br.com.docmatch.core

import java.text.NumberFormat
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

object Format {
    private val brl = NumberFormat.getCurrencyInstance(PtBr)
    private val dateTime = DateTimeFormatter.ofLocalizedDateTime(FormatStyle.SHORT).withLocale(PtBr)
    private val date = DateTimeFormatter.ofLocalizedDate(FormatStyle.SHORT).withLocale(PtBr)

    /** Centavos to "R$ 1.234,56" (negative values keep the sign). */
    fun money(cents: Long): String = brl.format(cents / 100.0)

    /** An RFC 3339 timestamp in the given zone, short pt-BR date and time. */
    fun dateTime(rfc3339: String?, zone: ZoneId = ZoneId.systemDefault()): String =
        rfc3339?.let { runCatching { dateTime.format(Instant.parse(it).atZone(zone)) }.getOrNull() } ?: ""

    /** An ISO date (2026-07-16) as 16/07/2026. */
    fun date(iso: String?): String = iso?.let { runCatching { date.format(LocalDate.parse(it.take(10))) }.getOrNull() } ?: ""

    /** Bytes the site's way (apps/web/src/lib/format.ts): binary units, one decimal under 10. */
    fun size(texts: Texts, bytes: Long): String {
        fun scaled(v: Double): String =
            NumberFormat.getNumberInstance(PtBr).apply { maximumFractionDigits = if (v < 10) 1 else 0 }.format(v)
        return when {
            bytes >= GB -> texts.t("files.size_gb", mapOf("n" to scaled(bytes.toDouble() / GB)))
            bytes >= MB -> texts.t("files.size_mb", mapOf("n" to scaled(bytes.toDouble() / MB)))
            bytes >= KB -> texts.t("files.size_kb", mapOf("n" to scaled(bytes.toDouble() / KB)))
            else -> texts.t("files.size_bytes", mapOf("n" to NumberFormat.getIntegerInstance(PtBr).format(bytes)))
        }
    }

    /**
     * An approximate duration the site's way (testdata/duration/cases.json): minutes rounded half
     * up, at least one; under an hour, minutes ("12 minutos"); up to ten hours, hours and minutes
     * in steps of five ("1 h 20 min"); from ten hours on, whole hours ("11 h").
     */
    fun duration(texts: Texts, seconds: Long): String {
        var minutes = maxOf(1L, (maxOf(seconds, 0L) + 30) / 60)
        if (minutes < 60) {
            return if (minutes == 1L) texts.t("duration.minute") else texts.t("duration.minutes", mapOf("minutes" to minutes))
        }
        minutes = if (minutes < 600) (minutes + 2) / 5 * 5 else (minutes + 30) / 60 * 60
        val hours = minutes / 60
        val rest = minutes % 60
        return if (rest == 0L) {
            texts.t("duration.hours", mapOf("hours" to hours))
        } else {
            texts.t("duration.hours_minutes", mapOf("hours" to hours, "minutes" to rest))
        }
    }

    /** The confirmation line of a large order (app.confirm_large); null for the others. */
    fun largeNotice(texts: Texts, quote: Quote): String? {
        val seconds = quote.expectedSeconds ?: 0L
        if (!quote.large || seconds <= 0) return null
        return texts.t("app.confirm_large", mapOf("duration" to duration(texts, seconds)))
    }

    private const val KB = 1024L
    private const val MB = 1024 * KB
    private const val GB = 1024 * MB
}
