package br.com.docmatch.core

import kotlin.test.assertEquals
import kotlin.test.assertNull
import org.junit.Test

class InvitationLinkTest {
    @Test
    fun onlyKnownDestinationsAndOneOpaqueTokenAreAccepted() {
        val token = "a".repeat(43)
        val site = "https://app.example.test"
        assertEquals(token, InvitationLink.token("$site/convite#token=$token", site))
        assertEquals(token, InvitationLink.token("docmatch://convite#token=$token", site))
        for (link in
            listOf(
                "https://evil.test/convite#token=$token",
                "https://me@app.example.test/convite#token=$token",
                "$site:444/convite#token=$token",
                "docmatch://other#token=$token",
                "$site/convite?token=$token",
                "docmatch://convite#token=$token&token=$token",
            )) assertNull(InvitationLink.token(link, site))
    }
}
