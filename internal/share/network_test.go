package share

import "testing"

func TestResolveAddress(t *testing.T) {
	addresses := []Address{
		{Interface: "enp3s0", IP: "192.168.1.42"},
		{Interface: "wlan0", IP: "10.0.0.8"},
	}
	got, err := ResolveAddress(addresses, "enp3s0", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.IP != "192.168.1.42" {
		t.Fatalf("got %+v", got)
	}
	got, err = ResolveAddress(addresses, "", "10.0.0.8")
	if err != nil {
		t.Fatal(err)
	}
	if got.Interface != "wlan0" {
		t.Fatalf("got %+v", got)
	}
	if _, err := ResolveAddress(addresses, "wlan0", "10.0.0.8"); err == nil {
		t.Fatal("both selectors should fail")
	}
	if _, err := ResolveAddress(addresses, "missing", ""); err == nil {
		t.Fatal("missing interface should fail")
	}
}

func TestResolveAddressRejectsAmbiguousInterface(t *testing.T) {
	addresses := []Address{
		{Interface: "enp3s0", IP: "192.168.1.42"},
		{Interface: "enp3s0", IP: "192.168.1.43"},
	}
	if _, err := ResolveAddress(addresses, "enp3s0", ""); err == nil {
		t.Fatal("multiple addresses should require --bind-ip")
	}
}
