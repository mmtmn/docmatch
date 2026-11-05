// Wiring for one install: the transport (cookies, User-Agent), the API, the flow and the texts.
package br.com.docmatch.core

import okhttp3.OkHttpClient

class Docmatch(
    baseUrl: String,
    store: SecretStore,
    userAgent: String,
    textsJson: String,
    client: OkHttpClient = OkHttpClient(),
    aliasLoopback: Boolean = false,
) {
    val texts = Texts(textsJson)
    val cookies = PersistentCookieJar(store)
    private val transport = HttpTransport(baseUrl, cookies, userAgent, texts, client, aliasLoopback)
    val api = DocmatchApi(transport)

    /** The session ended on the server (another device revoked it, for instance). */
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
