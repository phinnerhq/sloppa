package password

import (
	"testing"

	_ "modernc.org/sqlite"
)

func TestRoundTrip(t *testing.T) {
	h := Hash("correct horse")
	if !Verify("correct horse", h) || Verify("wrong", h) {
		t.Fatal("round trip failed")
	}
}

func TestRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=99999999,t=2,p=1$YQ$YQ", "$argon2id$v=19$m=65536,t=0,p=1$YQ$YQ"} {
		if Verify("x", h) {
			t.Errorf("%q verified", h)
		}
	}
}
