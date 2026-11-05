package br.com.docmatch.core

import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class DurationTest {
    private val texts = Texts(realTexts())

    /** testdata/duration/cases.json: the texts the site and the iOS app give too. */
    @Test
    fun `durations follow the shared cases`() {
        val root = generateSequence(java.io.File("").absoluteFile) { it.parentFile }.first { java.io.File(it, "testdata/duration/cases.json").exists() }
        val cases = DocmatchJson.parseToJsonElement(java.io.File(root, "testdata/duration/cases.json").readText()).jsonObject["cases"]!!.jsonArray
        assertTrue(cases.size >= 10)
        cases.forEach { c ->
            val seconds = c.jsonObject["seconds"]!!.jsonPrimitive.long
            assertEquals(c.jsonObject["text"]!!.jsonPrimitive.content, Format.duration(texts, seconds), "$seconds s")
        }
    }

    @Test
    fun `the quote reads the timing fields and tolerates their absence`() {
        val old = DocmatchJson.decodeFromString(Quote.serializer(), """{"id":"qte_1","expected_credits":12,"cap_credits":15,"signature":"v1=x"}""")
        assertNull(old.expectedSeconds)
        assertEquals(false, old.large)
        assertNull(Format.largeNotice(texts, old))

        val large = DocmatchJson.decodeFromString(
            Quote.serializer(),
            """{"id":"qte_2","expected_credits":12,"cap_credits":15,"signature":"v1=x","expected_seconds":4800,"large":true}""",
        )
        assertEquals(4800L, large.expectedSeconds)
        assertEquals("Pedido grande: deve levar cerca de 1 h 20 min. Avisamos por e-mail quando terminar.", Format.largeNotice(texts, large))

        // Small orders show nothing new, even with a duration; a large flag without one too.
        assertNull(Format.largeNotice(texts, large.copy(large = false)))
        assertNull(Format.largeNotice(texts, large.copy(expectedSeconds = null)))
    }
}
