package domain

import (
	"strings"
	"testing"
)

func TestBuildReleaseCandidateIdentityUsesBTIHBeforeURLs(t *testing.T) {
	identity, err := BuildReleaseCandidateIdentity(
		"DMHY",
		"Canonical Show 01",
		"magnet:?dn=Show&xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567",
		"https://example.test/topics/view/1",
	)
	if err != nil {
		t.Fatalf("BuildReleaseCandidateIdentity() error = %v", err)
	}
	if identity != "btih:0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("identity = %q", identity)
	}
}

func TestBuildReleaseCandidateIdentityCanonicalizesHTTPURL(t *testing.T) {
	identity, err := BuildReleaseCandidateIdentity(
		"mikan",
		"Canonical Show 02",
		"HTTPS://Example.TEST:443/files/show.torrent?b=2&a=1#fragment",
		"",
	)
	if err != nil {
		t.Fatalf("BuildReleaseCandidateIdentity() error = %v", err)
	}
	if identity != "url:https://example.test/files/show.torrent?a=1&b=2" {
		t.Fatalf("identity = %q", identity)
	}
}

func TestBuildReleaseCandidateIdentityFallsBackToStableTitleDigest(t *testing.T) {
	first, err := BuildReleaseCandidateIdentity("kisssub", "  Canonical   Show 03 ", "", "")
	if err != nil {
		t.Fatalf("first identity error = %v", err)
	}
	second, err := BuildReleaseCandidateIdentity("KISSSUB", "Canonical Show 03", "", "")
	if err != nil {
		t.Fatalf("second identity error = %v", err)
	}
	if first != second || !strings.HasPrefix(first, "title:") {
		t.Fatalf("identities = %q and %q", first, second)
	}
}
