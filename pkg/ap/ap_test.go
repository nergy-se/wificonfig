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

func TestParseScanResults(t *testing.T) {
	out := scanResultString + "aa:bb:cc:dd:ee:ff\t2412\t-70\t[WPA2-PSK-CCMP][ESS]\tmy home wifi\n" +
		"aa:bb:cc:dd:ee:00\t2412\t-70\t[WPA2-PSK-CCMP][ESS]\t\n"
	networks := parseScanResults(out)
	if len(networks) != 15 {
		t.Fatalf("expected 15 networks, got %d", len(networks))
	}
	if networks[0].Ssid != "Hokage5" || networks[14].Ssid != "my home wifi" {
		t.Errorf("unexpected ssids: %q %q", networks[0].Ssid, networks[14].Ssid)
	}
}
