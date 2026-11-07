// Wiring for one install: the transport (cookies, User-Agent), the API, the flow and the texts.
package br.com.docmatch.core

import okhttp3.OkHttpClient

class Docmatch private constructor(
    val texts: Texts,
    val cookies: PersistentCookieJar,
    private val transport: HttpTransport,
    val api: DocmatchApi,
) {
    constructor(baseUrl: String, store: SecretStore, userAgent: String, textsJson: String, client: OkHttpClient = OkHttpClient(), aliasLoopback: Boolean = false) :
        this(Texts(textsJson), PersistentCookieJar(store), baseUrl, userAgent, client, aliasLoopback)
    private constructor(texts: Texts, cookies: PersistentCookieJar, baseUrl: String, userAgent: String, client: OkHttpClient, aliasLoopback: Boolean) :
        this(texts, cookies, HttpTransport(baseUrl, cookies, userAgent, texts, client, aliasLoopback))
    private constructor(texts: Texts, cookies: PersistentCookieJar, transport: HttpTransport) :
        this(texts, cookies, transport, DocmatchApi(transport))
    fun inAccount(id: String) = Docmatch(texts, cookies, transport, api.inAccount(id))
    fun inProject(id: String) = Docmatch(texts, cookies, transport, api.inProject(id))
    val sessionEnded = transport.sessionEnded
    val reconciler = Reconciler(api, texts)
    val baseUrl: String = transport.base.toString().trimEnd('/')

    companion object {
        /** The User-Agent the API turns into "App Android · <model>" in every device list. */
        fun androidUserAgent(versionName: String, model: String, release: String): String =
            "docmatch-android/${ascii(versionName)} (${ascii(model)}; Android ${ascii(release)})"

        /** Header values must be printable ASCII (OkHttp refuses the rest); `;()` delimit the UA. */
        private fun ascii(s: String): String = s.map { if (it in ' '..'~' && it !in ";()") it else ' ' }.joinToString("").trim().replace(Regex(" +"), " ")
    }
}
