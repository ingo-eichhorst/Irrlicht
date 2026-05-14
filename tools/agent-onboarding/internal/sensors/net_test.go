package sensors

import "testing"

func TestParseLsofName_establishedConn(t *testing.T) {
	c, ok := parseLsofName("12345", "TCP", "192.168.1.5:54321->1.2.3.4:443 (ESTABLISHED)")
	if !ok {
		t.Fatal("parse failed")
	}
	if c.PID != 12345 || c.Proto != "TCP" || c.Host != "1.2.3.4" || c.Port != 443 || c.State != "ESTABLISHED" {
		t.Errorf("got %+v", c)
	}
}

func TestParseLsofName_listeningSocket(t *testing.T) {
	c, ok := parseLsofName("12345", "TCP", "*:8080")
	if !ok {
		t.Fatal("parse failed")
	}
	if c.Host != "*" || c.Port != 8080 || c.State != "" {
		t.Errorf("got %+v", c)
	}
}

func TestParseLsofName_ipv6(t *testing.T) {
	c, ok := parseLsofName("12345", "TCP", "[::1]:54321->[fe80::1]:443 (ESTABLISHED)")
	if !ok {
		t.Fatal("parse failed")
	}
	if c.Host != "[fe80::1]" || c.Port != 443 {
		t.Errorf("got %+v", c)
	}
}

func TestParseLsofName_invalidReturnsFalse(t *testing.T) {
	if _, ok := parseLsofName("abc", "TCP", "1.2.3.4:443"); ok {
		t.Error("expected false on bad pid")
	}
	if _, ok := parseLsofName("123", "TCP", "no-colon"); ok {
		t.Error("expected false on missing port")
	}
	if _, ok := parseLsofName("123", "TCP", "host:notaport"); ok {
		t.Error("expected false on non-numeric port")
	}
}
