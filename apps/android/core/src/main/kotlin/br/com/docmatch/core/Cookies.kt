// The session lives in the API's cookies (dm_session, dm_refresh bound to dm_device), the same
// device-bound session a browser gets. On the phone they are kept by a SecretStore (the app wraps
// it in the Android Keystore), never in plain preferences.
package br.com.docmatch.core

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl

/** Opaque storage for the serialized cookies; null clears it. */
interface SecretStore {
    fun read(): ByteArray?

    fun write(bytes: ByteArray?)
}

/** Keeps the cookies in memory (tests, previews). */
class MemorySecretStore : SecretStore {
    @Volatile private var bytes: ByteArray? = null

    override fun read(): ByteArray? = bytes?.copyOf()

    override fun write(bytes: ByteArray?) {
        this.bytes = bytes?.copyOf()
    }
}

@Serializable
private data class StoredCookie(
    val name: String,
    val value: String,
    val expiresAt: Long,
    val domain: String,
    val path: String,
    val secure: Boolean,
    val httpOnly: Boolean,
    val hostOnly: Boolean,
    val persistent: Boolean,
)

/**
 * A CookieJar persisted through a SecretStore: a new client (the next app launch) presents the
 * same session, and a refresh rotation replaces the stored refresh cookie atomically.
 */
class PersistentCookieJar(
    private val store: SecretStore,
    private val now: () -> Long = System::currentTimeMillis,
) : CookieJar {
    private val lock = Any()
    private var cookies: MutableList<Cookie> = load()

    override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
        synchronized(lock) {
            for (c in cookies) {
                this.cookies.removeAll { it.name == c.name && it.domain == c.domain && it.path == c.path }
                if (c.expiresAt > now()) this.cookies.add(c)
            }
            persist()
        }
    }

    override fun loadForRequest(url: HttpUrl): List<Cookie> =
        synchronized(lock) {
            val t = now()
            val before = cookies.size
            cookies.removeAll { it.expiresAt <= t }
            if (cookies.size != before) persist()
            cookies.filter { it.matches(url) }
        }

    /** Forgets every cookie (sign-out on this device). */
    fun clear() {
        synchronized(lock) {
            cookies.clear()
            store.write(null)
        }
    }

    /** Whether a session cookie is held (the account may still have revoked it server-side). */
    fun hasSession(): Boolean = synchronized(lock) { cookies.any { it.name == SESSION_COOKIE && it.expiresAt > now() } }

    private fun load(): MutableList<Cookie> {
        val bytes = store.read() ?: return mutableListOf()
        return runCatching {
            DocmatchJson.decodeFromString(ListSerializer(StoredCookie.serializer()), bytes.decodeToString())
                .filter { it.expiresAt > now() }
                .map { it.toCookie() }
                .toMutableList()
        }.getOrElse { mutableListOf() }
    }

    private fun persist() {
        val stored = cookies.map { it.toStored() }
        store.write(DocmatchJson.encodeToString(ListSerializer(StoredCookie.serializer()), stored).encodeToByteArray())
    }

    private fun Cookie.toStored() =
        StoredCookie(name, value, expiresAt, domain, path, secure, httpOnly, hostOnly, persistent)

    private fun StoredCookie.toCookie(): Cookie {
        val b = Cookie.Builder().name(name).value(value).expiresAt(expiresAt).path(path)
        if (hostOnly) b.hostOnlyDomain(domain) else b.domain(domain)
        if (secure) b.secure()
        if (httpOnly) b.httpOnly()
        return b.build()
    }

    companion object {
        const val SESSION_COOKIE = "dm_session"
    }
}
