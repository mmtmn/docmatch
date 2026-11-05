package br.com.docmatch.core

import br.com.docmatch.contracts.Limits
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.Cookie
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class TextsAndCookiesTest {
    private val texts = Texts(realTexts())

    @Test
    fun `the app reads the same pt-BR texts as the web, placeholders included`() {
        assertEquals("Conciliar", texts.t("form.submit"))
        assertEquals("Enviando 2 de 3 arquivo(s)…", texts.t("app.uploading", mapOf("done" to 2, "total" to 3)))
        assertEquals("9.960 tokens disponíveis", texts.t("account.credits_available", mapOf("available" to 9960)))
        assertEquals("chave.que.nao.existe", texts.t("chave.que.nao.existe"))
        assertTrue(texts.has("mobile.tab_new"))
    }

    @Test
    fun `money, dates and sizes the pt-BR way`() {
        assertEquals("R$ 49.000,00", Format.money(4_900_000).replace(' ', ' '))
        assertEquals("-R$ 12,34", Format.money(-1234).replace(' ', ' '))
        assertEquals("16/07/2026", Format.date("2026-07-16"))
        assertEquals("5,3 KB", Format.size(texts, 5_430))
        assertEquals("12 MB", Format.size(texts, 12_900_000), "acima de 10, sem casa decimal, como o site")
        assertEquals("512 B", Format.size(texts, 512))
        assertEquals("50 MB", Format.size(texts, 50L * 1024 * 1024))
        assertEquals("", Format.date("não é data"))
    }

    @Test
    fun `cookies persist, expire and clear`() {
        var now = 1_000_000L
        val store = MemorySecretStore()
        val url = "https://docmatch.invalid/v1/me".toHttpUrl()
        val jar = PersistentCookieJar(store) { now }
        val session = Cookie.Builder().name("dm_session").value("a").domain("docmatch.invalid").path("/").expiresAt(now + 60_000).httpOnly().secure().build()
        val refresh = Cookie.Builder().name("dm_refresh").value("r").domain("docmatch.invalid").path("/v1/auth/refresh").expiresAt(now + 60_000).build()
        jar.saveFromResponse(url, listOf(session, refresh))
        assertEquals(listOf("dm_session"), jar.loadForRequest(url).map { it.name })
        assertEquals(setOf("dm_session", "dm_refresh"), jar.loadForRequest("https://docmatch.invalid/v1/auth/refresh".toHttpUrl()).map { it.name }.toSet())

        val reopened = PersistentCookieJar(store) { now }
        assertTrue(reopened.hasSession())
        assertEquals("a", reopened.loadForRequest(url).single().value)

        // A rotation replaces, never duplicates.
        reopened.saveFromResponse(url, listOf(session.newBuilderCopy(value = "b", expiresAt = now + 60_000)))
        assertEquals("b", PersistentCookieJar(store) { now }.loadForRequest(url).single().value)

        now += 120_000
        assertEquals(emptyList(), PersistentCookieJar(store) { now }.loadForRequest(url))
        reopened.clear()
        assertEquals(null, store.read())
    }

    private fun Cookie.newBuilderCopy(value: String, expiresAt: Long): Cookie =
        Cookie.Builder().name(name).value(value).domain(domain).path(path).expiresAt(expiresAt).httpOnly().secure().build()
}

class PrecheckTest {
    private val texts = Texts(realTexts())

    /** testdata/precheck/cases.json: the verdicts the site and the iOS app give too. */
    @Test
    fun `the shared cases get the site's verdict and MIME`() {
        val root = generateSequence(java.io.File("").absoluteFile) { it.parentFile }.first { java.io.File(it, "testdata/precheck/cases.json").exists() }
        val cases = DocmatchJson.parseToJsonElement(java.io.File(root, "testdata/precheck/cases.json").readText()).jsonObject["cases"]!!.jsonArray
        assertTrue(cases.size >= 20)
        for (c in cases.map { it.jsonObject }) {
            val name = c["name"]!!.jsonPrimitive.content
            val head = c["hex"]!!.jsonPrimitive.content.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
            val (kind, refusal) = Precheck.classify(name, head)
            assertEquals(c["kind"]?.jsonPrimitive?.content, kind?.id, name)
            assertEquals(c["reason"]?.jsonPrimitive?.content, refusal?.key, name)
            if (kind != null) assertEquals(c["mime"]?.jsonPrimitive?.content, Precheck.mime(kind, name), name)
        }
    }

    private fun candidate(name: String, size: Long, head: String = "a;b\n1;2\n") = Candidate(name, size) { head.encodeToByteArray() }

    @Test
    fun `the picker's files are checked in the site's order with its messages`() {
        val existing = listOf(LocalFile.of("ja.csv", "a;b\n".encodeToByteArray()))
        val (accepted, problems) =
            Precheck.check(
                texts,
                listOf(
                    candidate("ok.csv", 10),
                    candidate("vazio.csv", 0),
                    candidate("ja.csv", 4),
                    candidate("ilegivel.csv", 10).let { Candidate(it.name, 10) { throw java.io.IOException("x") } },
                    candidate("foto.png", 10, "\u0089PNG"),
                    candidate("grande.csv", Precheck.documentFileBytes + 1),
                    candidate("ok.csv", 11),
                ),
                existing,
            )
        assertEquals(listOf("ok.csv", "ok.csv"), accepted.map { it.name }, "mesmo nome e outro tamanho é outro arquivo")
        assertEquals(
            listOf(
                texts.t("errors.file_empty", mapOf("name" to "vazio.csv")),
                texts.t("errors.file_duplicate", mapOf("name" to "ja.csv")),
                texts.t("errors.read_failed", mapOf("name" to "ilegivel.csv")),
                texts.t("errors.file_extension_mismatch", mapOf("name" to "foto.png", "ext" to "png")),
                texts.t("errors.file_too_large", mapOf("name" to "grande.csv", "max" to "50 MB")),
            ),
            problems,
        )
        val many = (1..Limits.Ids.FILES_PER_RUN_HARD_MAX + 1).map { candidate("f$it.csv", 10) }
        val (some, refusals) = Precheck.check(texts, many, emptyList())
        assertEquals(Limits.Ids.FILES_PER_RUN_HARD_MAX, some.size)
        assertEquals(listOf(texts.t("errors.too_many_files", mapOf("max" to Limits.Ids.FILES_PER_RUN_HARD_MAX))), refusals)
    }

    @Test
    fun `images are refused until the API takes them`() {
        val png = byteArrayOf(0x89.toByte(), 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)
        val (accepted, problems) = Precheck.check(texts, listOf(Candidate("foto.png", 8) { png }), emptyList())
        assertTrue(accepted.isEmpty())
        assertEquals(listOf(texts.t("mobile.image_unsupported", mapOf("name" to "foto.png"))), problems)
    }
}
