package br.com.docmatch.core

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
        assertEquals("5,3 KB", Format.size(5_430))
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

class FileTypesTest {
    private val texts = Texts(realTexts())

    @Test
    fun `the app accepts and refuses the same documents as the site`() {
        assertEquals("text/csv", FileTypes.mimeFor("Extrato JULHO.CSV"))
        assertEquals("application/x-ofx", FileTypes.mimeFor("banco.ofx"))
        assertEquals(null, FileTypes.mimeFor("planilha.numbers"))
        assertEquals(null, FileTypes.problem(texts, "a.csv", 10))
        assertEquals("a.csv: o arquivo está vazio.", FileTypes.problem(texts, "a.csv", 0))
        assertEquals("a.csv: maior que o limite de 50 MB.", FileTypes.problem(texts, "a.csv", FileTypes.maxDocumentBytes + 1))
        assertTrue(FileTypes.problem(texts, "x.numbers", 10)!!.startsWith("x.numbers: formato não reconhecido"))
    }
}
