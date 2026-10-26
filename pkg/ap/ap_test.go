package ap

import "testing"

var scanResultString = `bssid / frequency / signal level / flags / ssid
18:e8:29:c2:8f:84	5180	-63	[WPA2-PSK-CCMP][ESS]	Hokage5
1e:e8:29:c2:8f:84	5180	-63	[WPA2-PSK-CCMP][ESS]	chromecast
26:e8:29:c2:8f:84	5180	-64	[WPA2-PSK-CCMP][ESS]	house
22:e8:29:c2:8f:84	5180	-64	[WPA2-PSK-CCMP][ESS]	iot
56:d9:e7:f3:91:77	2462	-57	[WPA2-PSK-CCMP][ESS]	house
44:d9:e7:f3:91:77	2462	-57	[WPA2-PSK-CCMP][ESS]	Hokage24
4a:d9:e7:f3:91:77	2462	-57	[WPA2-PSK-CCMP][ESS]	hguest
4e:d9:e7:f3:91:77	2462	-57	[WPA2-PSK-CCMP][ESS]	chromecast
52:d9:e7:f3:91:77	2462	-57	[WPA2-PSK-CCMP][ESS]	iot
22:e8:29:c1:8f:84	2412	-67	[WPA2-PSK-CCMP][ESS]	chromecast
26:e8:29:c1:8f:84	2412	-67	[WPA2-PSK-CCMP][ESS]	iot
2a:e8:29:c1:8f:84	2412	-67	[WPA2-PSK-CCMP][ESS]	house
1e:e8:29:c1:8f:84	2412	-68	[WPA2-PSK-CCMP][ESS]	hguest
18:e8:29:c1:8f:84	2412	-71	[WPA2-PSK-CCMP][ESS]	Hokage24
`

func TestParseListNetworks(t *testing.T) {
	out := "network id / ssid / bssid / flags\n0\tNergy setup\tany\t[DISABLED]\n1\tcool ssid\tany\t[CURRENT]\n2\told\tany"
	networks := parseListNetworks(out)
	if len(networks) != 3 {
		t.Fatalf("expected 3 networks got %d", len(networks))
	}
	if networks[0].Ssid != "Nergy setup" || !networks[0].Disabled() {
		t.Errorf("unexpected AP network %+v", networks[0])
	}
	if networks[1].Ssid != "cool ssid" || networks[1].Disabled() || networks[1].Flags != "[CURRENT]" {
		t.Errorf("unexpected station network %+v", networks[1])
	}
	if networks[2].ID != "2" || networks[2].Flags != "" {
		t.Errorf("unexpected network %+v", networks[2])
	}
}

func TestParseScanResults(t *testing.T) {
	networks := parseScanResults(scanResultString)
	if len(networks) != 14 {
		t.Fatalf("expected 14 networks got %d", len(networks))
	}
	if n := networks[0]; n.Bssid != "18:e8:29:c2:8f:84" || n.Frequency != "5180" || n.SignalLevel != "-63" || n.Flags != "[WPA2-PSK-CCMP][ESS]" || n.Ssid != "Hokage5" {
		t.Errorf("unexpected first network %+v", n)
	}

	out := "bssid / frequency / signal level / flags / ssid\n" +
		"aa:aa:aa:aa:aa:aa\t2412\t-60\t[WPA2-PSK-CCMP][ESS]\tOffice  WiFi\n" +
		"bb:bb:bb:bb:bb:bb\t2412\t-60\t[WPA2-PSK-CCMP][ESS]\t\n" +
		"cc:cc:cc:cc:cc:cc\t2412\t-60\t[WPA2-PSK-CCMP][ESS][P2P]\tDIRECT-xy"
	networks = parseScanResults(out)
	if len(networks) != 1 || networks[0].Ssid != "Office  WiFi" {
		t.Errorf("expected only Office  WiFi, hidden and P2P skipped, got %+v", networks)
	}
}
