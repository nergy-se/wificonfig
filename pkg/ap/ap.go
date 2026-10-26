package ap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nergy-se/wificonfig/pkg/commands"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

type Ap struct {
	/* data */
	dnsmasq       *exec.Cmd
	wpasupplicant *exec.Cmd

	wpaSupplicantConfigFile   string
	ip                        string
	dhcpStart                 string
	dhcpEnd                   string
	EthernetInterfaceName     string
	wiredStaticConfigLocation string
	apSSID                    string

	mutex             sync.Mutex
	lastManualConnect time.Time

	// wpaMu serializes the wpa_cli sequences that change networks or scan, since
	// the webserver and the reconcile loop run them from different goroutines.
	wpaMu sync.Mutex
}

func New(c *cli.Context) *Ap {
	return &Ap{
		EthernetInterfaceName:     c.String("ethernet-interface"),
		wpaSupplicantConfigFile:   c.String("wpa-supplicant-config"),
		ip:                        c.String("ap-ip"),
		dhcpStart:                 c.String("dhcp-start"),
		dhcpEnd:                   c.String("dhcp-end"),
		wiredStaticConfigLocation: c.String("wired-static-config-location"),
		apSSID:                    c.String("ap-ssid"),
	}
}

func (a *Ap) StopDnsmasq() error {
	if cmd := a.DnsMasqCmd(); cmd != nil {
		logrus.Debug("stopping dnsmasq")
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

func (a *Ap) StartDnsmasq(ctx context.Context) error {
	if cmd := a.DnsMasqCmd(); cmd != nil {
		return nil // already running
	}
	args := []string{
		"--no-hosts", // Don't read the hostnames in /etc/hosts.
		"--keep-in-foreground",
		"--log-queries",
		"--no-resolv",
		"--address=/#/" + a.ip,
		fmt.Sprintf("--dhcp-range=%s,%s,1h", a.dhcpStart, a.dhcpEnd),
		"--dhcp-authoritative",
		"--log-facility=-", // log to stderr
	}

	logrus.Debug(append([]string{"starting: dnsmasq"}, args...))
	cmd := exec.CommandContext(ctx, "dnsmasq", args...)
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("error starting dnsmasq: %w", err)
	}
	a.mutex.Lock()
	a.dnsmasq = cmd
	a.mutex.Unlock()
	go func() {
		err := cmd.Wait()
		if err != nil {
			logrus.Error(err)
		}

		a.mutex.Lock()
		a.dnsmasq = nil
		a.mutex.Unlock()
	}()
	return err
}

func (a *Ap) StopWpaSupplicant() error {
	if cmd := a.WpaCmd(); cmd != nil {
		logrus.Debug("stopping wpa_supplicant")
		return cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

func (a *Ap) WpaCmd() *exec.Cmd {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.wpasupplicant
}
func (a *Ap) DnsMasqCmd() *exec.Cmd {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.dnsmasq
}

// StartWpaSupplicant starts wpa_supplicant if it is not running and reports if it did.
func (a *Ap) StartWpaSupplicant(ctx context.Context) (bool, error) {
	if a.WpaCmd() != nil {
		return false, nil
	}

	logrus.Debug("starting wpa_supplicant")

	args := []string{
		"-Dnl80211",
		"-iwlan0",
		"-c" + a.wpaSupplicantConfigFile,
	}

	logrus.Debug(append([]string{"starting: wpa_supplicant"}, args...))
	cmd := exec.CommandContext(ctx, "wpa_supplicant", args...)
	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}

	scanner := bufio.NewScanner(cmdReader)
	go func() {
		for scanner.Scan() {
			logrus.Infof("wpa_supplicant said: %s\n", scanner.Text())
		}
	}()
	err = cmd.Start()
	if err != nil {
		return false, err
	}

	a.mutex.Lock()
	a.wpasupplicant = cmd
	a.mutex.Unlock()

	go func() {
		err := cmd.Wait()
		if err != nil {
			logrus.Error(err)
		}

		a.mutex.Lock()
		a.wpasupplicant = nil
		a.mutex.Unlock()
	}()

	return true, nil
}

// WaitForWpa waits until wpa_supplicant answers on its control socket.
func (a *Ap) WaitForWpa(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := wpaCli("ping")
		if err == nil && out == "PONG" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wpa_supplicant not answering after %s: %s %v", timeout, out, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// wpaCli runs a wpa_cli command on wlan0. wpa_cli exits 0 also when the command
// fails, so a FAIL or UNKNOWN COMMAND reply is returned as an error.
func wpaCli(args ...string) (string, error) {
	out, err := commands.Run("wpa_cli", append([]string{"-i", "wlan0"}, args...)...)
	if err != nil {
		return out, err
	}
	if strings.HasPrefix(out, "FAIL") || strings.HasPrefix(out, "UNKNOWN COMMAND") {
		return out, fmt.Errorf("wpa_cli %s: %s", strings.Join(args, " "), out)
	}
	return out, nil
}

func (a *Ap) EnsureWpaNetworkAdded() (string, error) {

	type row struct {
		ID      string
		Name    string
		Current bool
	}
	networks := []row{}

	networkListOut, err := commands.Run("wpa_cli", "-i", "wlan0", "list_networks")
	if err != nil {
		return "", err
	}
	tmp := strings.Split(string(networkListOut), "\n")
	for _, netRecord := range tmp[1:] {

		fields := strings.Fields(netRecord)

		current := false
		if len(fields) > 3 && strings.Contains(fields[3], "CURRENT") {
			current = true
		}

		if len(fields) > 1 {
			networks = append(networks, row{
				ID:      fields[0],
				Name:    fields[1],
				Current: current,
			})
		}
	}

	if len(networks) == 1 && networks[0].ID == "0" { // we need to add our first network.
		net, err := commands.Run("wpa_cli", "-i", "wlan0", "add_network")
		if err != nil {
			return "", err
		}
		logrus.Infof("add_network: %s", net)
		return net, nil
	}
	if len(networks) > 1 {
		return networks[1].ID, nil
	}

	return "", nil
}

// LastManualConnect returns when a network was last submitted from the setup page.
func (a *Ap) LastManualConnect() time.Time {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.lastManualConnect
}

func (a *Ap) ConnectToNetwork(ssid, key string) error {
	a.wpaMu.Lock()
	defer a.wpaMu.Unlock()

	apWasEnabled, err := a.apNetworkEnabled()
	if err != nil {
		apWasEnabled = true // keep the AP enabled if we do not know
	}

	net, err := a.EnsureWpaNetworkAdded()
	if err != nil {
		return err
	}

	response, err := wpaCli("set_network", net, "ssid", "\""+ssid+"\"")
	if err != nil {
		return err
	}
	logrus.Infof("set_network ssid: %s", response)

	response, err = wpaCli("set_network", net, "psk", "\""+key+"\"")
	if err != nil {
		return err
	}
	logrus.Infof("set_network psk: %s", response)

	response, err = wpaCli("set_network", net, "key_mgmt", "WPA-PSK")
	if err != nil {
		return err
	}
	logrus.Infof("set_network key_mgmt: %s", response)

	response, err = wpaCli("set_network", net, "priority", "10")
	if err != nil {
		return err
	}
	logrus.Infof("set_network priority: %s", response)

	response, err = wpaCli("enable_network", net)
	if err != nil {
		return err
	}
	logrus.Infof("enable_network: %s", response)

	// The AP network may be disabled at runtime (see SetAPNetworkEnabled). Enable it before
	// saving so the file always keeps it enabled; an older wificonfig after a rollback
	// would otherwise never get a setup AP again. reconcile disables it again when needed.
	// Still save the submitted network if this fails, so it is not lost on reboot.
	err = a.setAPNetworkEnabled(true)
	if err != nil {
		logrus.Warnf("error enabling AP network before save_config: %s", err)
	}

	// reconfigure loads the AP enabled from the file. Restore what it was before: disabled
	// (on wifi, the setup page used over the LAN) so one missed scan does not make us an AP,
	// enabled (we are an AP, or reconcile wants one) so a wrong PSK falls back to the AP and can
	// be fixed at once. Deferred so it also happens when saving fails.
	if !apWasEnabled {
		defer func() {
			if err := a.setAPNetworkEnabled(false); err != nil {
				logrus.Warnf("error disabling AP network after connect: %s", err)
			}
		}()
	}

	response, err = wpaCli("save_config", net)
	if err != nil {
		return err
	}
	logrus.Infof("save_config: %s", response)

	// Recorded once the network is saved, so a rejected submit does not pause the AP retries.
	// Before reconfigure, so reconcile does not leave the AP that reconfigure may fall back to.
	a.mutex.Lock()
	a.lastManualConnect = time.Now()
	a.mutex.Unlock()

	response, err = wpaCli("reconfigure", net)
	if err != nil {
		return err
	}
	logrus.Infof("reconfigure: %s", response)
	return nil
}

type WpaNetwork struct {
	Bssid       string `json:"bssid"`
	Frequency   string `json:"frequency"`
	SignalLevel string `json:"signalLevel"`
	Flags       string `json:"flags"`
	Ssid        string `json:"ssid"`
}

func (a *Ap) ScanNetworks() ([]*WpaNetwork, error) {
	return a.scanNetworks(1 * time.Second)
}

func (a *Ap) scanNetworks(wait time.Duration) ([]*WpaNetwork, error) {
	a.wpaMu.Lock() // held during the wait, so a second scan waits instead of getting FAIL-BUSY
	defer a.wpaMu.Unlock()

	wpaNetworks := []*WpaNetwork{}

	scanOut, err := wpaCli("scan")
	if err != nil {
		return wpaNetworks, err
	}

	if scanOut != "OK" {
		return wpaNetworks, fmt.Errorf("expected OK from wpa_cli scan got: %s", scanOut)
	}
	time.Sleep(wait)
	networkListOut, err := wpaCli("scan_results")
	if err != nil {
		return wpaNetworks, err
	}

	return parseScanResults(networkListOut), nil
}

// parseScanResults parses the tab separated output of wpa_cli scan_results.
func parseScanResults(out string) []*WpaNetwork {
	wpaNetworks := []*WpaNetwork{}
	tmp := strings.Split(out, "\n")
	for _, netRecord := range tmp[1:] {
		if strings.Contains(netRecord, "[P2P]") {
			continue
		}

		fields := strings.Split(netRecord, "\t") // tab separated so SSIDs keep their spaces

		if len(fields) > 4 && fields[4] != "" { // hidden networks have no SSID
			wpaNetworks = append(wpaNetworks, &WpaNetwork{
				Bssid:       fields[0],
				Frequency:   fields[1],
				SignalLevel: fields[2],
				Flags:       fields[3],
				Ssid:        fields[4],
			})
		}
	}

	return wpaNetworks
}

type WpaListedNetwork struct {
	ID    string
	Ssid  string
	Flags string
}

func (n WpaListedNetwork) Disabled() bool {
	return strings.Contains(n.Flags, "[DISABLED]")
}

// parseListNetworks parses the tab separated output of wpa_cli list_networks.
func parseListNetworks(out string) []WpaListedNetwork {
	networks := []WpaListedNetwork{}
	rows := strings.Split(out, "\n")
	for _, row := range rows[1:] {
		fields := strings.Split(row, "\t")
		if len(fields) < 2 {
			continue
		}
		n := WpaListedNetwork{ID: fields[0], Ssid: fields[1]}
		if len(fields) > 3 {
			n.Flags = fields[3]
		}
		networks = append(networks, n)
	}
	return networks
}

func (a *Ap) listNetworks() ([]WpaListedNetwork, error) {
	out, err := wpaCli("list_networks")
	if err != nil {
		return nil, err
	}
	return parseListNetworks(out), nil
}

// StationNetworks returns the enabled networks we can connect to as a client, that is all except our own AP.
func (a *Ap) StationNetworks() ([]WpaListedNetwork, error) {
	networks, err := a.listNetworks()
	if err != nil {
		return nil, err
	}
	stations := []WpaListedNetwork{}
	for _, n := range networks {
		if n.Ssid != a.apSSID && !n.Disabled() {
			stations = append(stations, n)
		}
	}
	return stations, nil
}

// SetAPNetworkEnabled enables or disables our own AP network in the running wpa_supplicant.
// While it is enabled wpa_supplicant falls back to AP mode as soon as one scan
// does not find a station network, and it never scans for one again while in AP mode.
// It does nothing if a network was submitted from the setup page after seen (see manualSince).
func (a *Ap) SetAPNetworkEnabled(enabled bool, seen time.Time) error {
	a.wpaMu.Lock()
	defer a.wpaMu.Unlock()
	if a.manualSince(seen) {
		return nil
	}
	return a.setAPNetworkEnabled(enabled)
}

// manualSince reports if a network was submitted from the setup page after seen. The caller
// decided what to do with seen as the last submit it knew of; a ConnectToNetwork that ran in
// between has set the AP network the way that submit needs, so the decision is stale.
// Must be called with wpaMu held, which ConnectToNetwork holds while it records the submit.
func (a *Ap) manualSince(seen time.Time) bool {
	return a.LastManualConnect().After(seen)
}

// apNetworkEnabled reports if our AP network is enabled in the running wpa_supplicant.
func (a *Ap) apNetworkEnabled() (bool, error) {
	networks, err := a.listNetworks()
	if err != nil {
		return false, err
	}
	for _, n := range networks {
		if n.Ssid == a.apSSID {
			return !n.Disabled(), nil
		}
	}
	return true, nil // nothing to disable, so treat it as enabled
}

func (a *Ap) setAPNetworkEnabled(enabled bool) error {
	networks, err := a.listNetworks()
	if err != nil {
		return err
	}
	for _, n := range networks {
		// matched by SSID, not by id, so we never disable a station network that is not where we expect it.
		if n.Ssid != a.apSSID || n.Disabled() != enabled {
			continue
		}
		cmd := "disable_network"
		if enabled {
			cmd = "enable_network"
		}
		logrus.Infof("%s %s (%s)", cmd, n.ID, n.Ssid)
		_, err := wpaCli(cmd, n.ID)
		return err
	}
	return nil
}

// LeaveAP disables our AP network and makes wpa_supplicant look for a station network.
// Disabling the current network only deauthenticates it, so we ask for a reassociate as well.
// dnsmasq is stopped first, otherwise it would answer DHCP and DNS on the station network
// (wlan0 keeps the AP IP until the next reconcile runs networkctl reconfigure).
// It reports false and does nothing if a network was submitted from the setup page after seen.
func (a *Ap) LeaveAP(seen time.Time) (bool, error) {
	a.wpaMu.Lock()
	defer a.wpaMu.Unlock()
	if a.manualSince(seen) {
		return false, nil
	}
	err := a.StopDnsmasq()
	if err != nil {
		return false, err
	}
	err = a.setAPNetworkEnabled(false)
	if err != nil {
		return false, err
	}
	_, err = wpaCli("reassociate")
	return err == nil, err
}

// VisibleSSID scans and returns the first of ssids that is seen, or "" if none is.
func (a *Ap) VisibleSSID(ssids []string) (string, error) {
	networks, err := a.scanNetworks(5 * time.Second)
	if err != nil {
		return "", err
	}
	for _, ssid := range ssids {
		for _, n := range networks {
			if n.Ssid == ssid {
				return ssid, nil
			}
		}
	}
	return "", nil
}

func (a *Ap) WpaIsAp() (bool, error) {
	response, err := commands.Run("wpa_cli", "-i", "wlan0", "status")
	if err != nil {
		return false, err
	}
	expectedStrings := []string{
		"ssid=",
		"mode=AP",
		"wpa_state=COMPLETED",
		// "ip_address=",
	}
	for _, str := range expectedStrings {
		if !strings.Contains(response, str) {
			return false, nil
		}
	}
	return true, nil
}
func (a *Ap) WpaConnectedToWifi() (bool, error) {
	response, err := commands.Run("wpa_cli", "-i", "wlan0", "status")
	if err != nil {
		return false, err
	}
	expectedStrings := []string{
		"ssid=",
		"wpa_state=COMPLETED",
		"mode=station",
		// "ip_address=",
	}
	for _, str := range expectedStrings {
		if !strings.Contains(response, str) {
			return false, nil
		}
	}
	return true, nil
}

func (a *Ap) GetConnectedSSID() (string, error) {
	response, err := commands.Run("wpa_cli", "-i", "wlan0", "status")
	if err != nil {
		return "", err
	}
	expectedStrings := []string{
		"ssid=",
		"wpa_state=COMPLETED",
		"mode=station",
		// "ip_address=",
	}
	rows := strings.Split(response, "\n")
	for _, str := range expectedStrings {
		if !strings.Contains(response, str) {
			return "", nil
		}
	}
	for _, str := range rows {
		if strings.HasPrefix(str, "ssid=") {
			tmp := strings.Split(str, "=")
			return tmp[1], nil
		}
	}
	return "", nil
}

func (a *Ap) EnsureEthernetStaticIP(ipWithMask, gateway, dns1, dns2, ntp string) error {
	ipWithMask = strings.TrimSpace(ipWithMask)
	gateway = strings.TrimSpace(gateway)
	dns1 = strings.TrimSpace(dns1)
	dns2 = strings.TrimSpace(dns2)
	ntp = strings.TrimSpace(ntp)

	fn := a.wiredStaticConfigLocation

	_, _, err := net.ParseCIDR(ipWithMask) // check valid syntax ip/mask for systemd-networkd config
	if err != nil {
		return err
	}

	var stringsToConfigure []string

	if ipWithMask != "" {
		stringsToConfigure = append(stringsToConfigure, "Address="+ipWithMask)
	}
	if gateway != "" {
		stringsToConfigure = append(stringsToConfigure, "Gateway="+gateway)
	}
	if dns1 != "" {
		stringsToConfigure = append(stringsToConfigure, "DNS="+dns1)
	}
	if dns2 != "" {
		stringsToConfigure = append(stringsToConfigure, "DNS="+dns2)
	}
	if ntp != "" {
		stringsToConfigure = append(stringsToConfigure, "NTP="+ntp)
	}

	writeConfigToFile := func(fn string) error {

		f, err := os.OpenFile(fn, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		content := fmt.Sprintf(`[Match]
Name=%s

[Network]
`, a.EthernetInterfaceName)

		for _, line := range stringsToConfigure {
			content += line + "\n"
		}

		_, err = io.WriteString(f, content)
		if err != nil {
			return err
		}

		// Also write copy to systemd folder if we dont already have that configured.
		// used when the rootfs is overwritten by an upgrade.
		if !strings.HasPrefix(a.wiredStaticConfigLocation, "/etc/systemd/network") {
			dstFn := filepath.Join("/etc/systemd/network", filepath.Base(a.wiredStaticConfigLocation))
			file, err := os.OpenFile(dstFn, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.WriteString(file, content)
			if err != nil {
				return err
			}
		}

		_, err = commands.Run("networkctl", "reload")
		return err
	}

	_, err = os.Stat(fn)
	if err == nil { // file exists check that it contains our IP otherwise override it.

		if ipWithMask == "" { //unconfigure it if it exists and we get call with empty ip.
			err = os.Remove(fn)
			if err != nil {
				return err
			}
			dstFn := filepath.Join("/etc/systemd/network", filepath.Base(a.wiredStaticConfigLocation))
			_ = os.Remove(dstFn) // just ignore the error
			_, err = commands.Run("networkctl", "reload")
			return err
		}

		var content []byte
		content, err = os.ReadFile(fn)
		if err != nil {
			return err
		}

		lines := strings.Split(string(content), "\n")

		for _, lookFor := range stringsToConfigure {
			found := false
			for _, line := range lines {
				if line == lookFor {
					found = true
					break
				}
			}
			if !found {
				return writeConfigToFile(fn)
			}
		}

		return nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return writeConfigToFile(fn)
	} else {
		return err
	}

}
