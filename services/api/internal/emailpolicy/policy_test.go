package emailpolicy

import "testing"

func TestDisposable(t *testing.T) {
	for _, email := range []string{"x@mailinator.com", "X@sub.MAILINATOR.COM", "x@guerrillamail.com", "x@yopmail.com"} {
		if !Disposable(email) {
			t.Errorf("accepted %s", email)
		}
	}
	for _, email := range []string{"x@gmail.com", "x@privaterelay.appleid.com", "x@notmailinator.example", "x@mailinator.com.example.org", "dev@docmatch.test"} {
		if Disposable(email) {
			t.Errorf("blocked %s", email)
		}
	}
}
