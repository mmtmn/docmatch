// The pt-BR texts of packages/locales/pt-BR.json, the same file the web uses, with the same
// `{name}` placeholders and pt-BR number formatting: the app says what the site says.
package br.com.docmatch.core

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import java.text.NumberFormat
import java.util.Locale

/** pt-BR, the only language of docmatch. */
val PtBr: Locale = Locale.forLanguageTag("pt-BR")

class Texts(json: String) {
    private val messages: Map<String, String> = flatten(DocmatchJson.parseToJsonElement(json))
    private val numbers = NumberFormat.getIntegerInstance(PtBr)

    /** The message for `key` with `{name}` replaced; the key itself when it is missing. */
    fun t(key: String, vars: Map<String, Any> = emptyMap()): String {
        val template = messages[key] ?: return key
        if (vars.isEmpty()) return template
        return PLACEHOLDER.replace(template) { m ->
            when (val v = vars[m.groupValues[1]]) {
                null -> m.value
                is Int, is Long -> numbers.format(v)
                else -> v.toString()
            }
        }
    }

    fun has(key: String): Boolean = key in messages

    private companion object {
        val PLACEHOLDER = Regex("\\{([a-z_]+)\\}")

        fun flatten(node: JsonElement, prefix: String = "", out: MutableMap<String, String> = mutableMapOf()): Map<String, String> {
            when (node) {
                is JsonObject -> node.forEach { (k, v) -> flatten(v, if (prefix.isEmpty()) k else "$prefix.$k", out) }
                is JsonPrimitive -> if (node.isString) out[prefix] = node.content
                else -> Unit // lists and tables are web-only
            }
            return out
        }
    }
}
