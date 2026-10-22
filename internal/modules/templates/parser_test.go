package templates

import (
	"strings"
	"testing"
)

func TestParseRequiresSlogan(t *testing.T) {
	_, err := Parse([]byte("services:\n  a:\n    image: nginx\n"), "noslogan", "t")
	if err == nil || !strings.Contains(err.Error(), "slogan") {
		t.Fatalf("missing slogan: %v", err)
	}
}

func TestClassifyURLRefusal(t *testing.T) {
	_, err := classify("CODEPLO_URL")
	if err == nil {
		t.Fatal("expected URL refusal")
	}
	_, err = classify("CODEPLO_PASSWORD")
	if err != nil {
		t.Fatalf("password fill should work: %v", err)
	}
	_, err = classify("SERVICE_FQDN")
	if err == nil {
		t.Fatal("SERVICE_FQDN should refuse")
	}
}
