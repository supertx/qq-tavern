package netguard

import (
	"net"
	"testing"
)

func TestBlockedAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "100.64.0.1", "fc00::1", "2002:7f00:1::"} {
		if PublicIP(net.ParseIP(ip)) {
			t.Fatalf("allowed %s", ip)
		}
	}
	if !PublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address blocked")
	}
}
func TestURLValidation(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "http://localhost/a", "http://user:pass@example.com", "http://127.0.0.1", "https://example.com:8000"} {
		if Validate(u) == nil {
			t.Fatalf("allowed %s", u)
		}
	}
	if err := Validate("https://example.com/page"); err != nil {
		t.Fatal(err)
	}
}
