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

    /** Bytes as "5,3 KB" / "1,2 MB". */
    fun size(bytes: Long): String {
        val nf = NumberFormat.getNumberInstance(PtBr).apply { maximumFractionDigits = 1 }
        return when {
            bytes < 1024 -> "$bytes B"
            bytes < 1024 * 1024 -> "${nf.format(bytes / 1024.0)} KB"
            else -> "${nf.format(bytes / (1024.0 * 1024.0))} MB"
        }
    }
}
