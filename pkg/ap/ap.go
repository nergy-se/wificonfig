package ap

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
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

	wpaSupplicantConfigFile string
	ip                      string
	dhcpStart               string
	dhcpEnd                 string

	mutex sync.Mutex
}

func New(c *cli.Context) *Ap {
	return &Ap{
		wpaSupplicantConfigFile: c.String("wpa-supplicant-config"),
		ip:                      c.String("ap-ip"),
		dhcpStart:               c.String("dhcp-start"),
		dhcpEnd:                 c.String("dhcp-end"),
	}
}

func (a *Ap) StopDnsmasq() error {
	if cmd := a.DnsMasqCmd(); cmd != nil {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

func (a *Ap) StartDnsmasq(ctx context.Context) error {
	if cmd := a.DnsMasqCmd(); cmd != nil {
		err := a.StopDnsmasq()
		return err
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

func (a *Ap) StartWpaSupplicant(ctx context.Context) error {
	if a.WpaCmd() != nil {
		return nil
	}

	args := []string{
		"-Dnl80211",
		"-iwlan0",
		"-c" + a.wpaSupplicantConfigFile,
	}

	cmd := exec.CommandContext(ctx, "wpa_supplicant", args...)
	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(cmdReader)
	go func() {
		for scanner.Scan() {
			fmt.Printf("wpa_supplicant said: %s\n", scanner.Text())
		}
	}()
	err = cmd.Start()
	if err != nil {
		return err
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

	return err
}

// ConfiguredNetworks returns a list of configured wifi networks.
func (a *Ap) ConfiguredNetworks(ctx context.Context) (string, error) {
	netOut, err := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "scan").Output()
	if err != nil {
		return "", err
	}

	return string(netOut), nil
}

func (a *Ap) EnsureWpaNetworkAdded() (string, error) {

	type row struct {
		ID      string
		Name    string
		Current bool
	}
	networks := []row{}

	networkListOut, err := exec.Command("wpa_cli", "-i", "wlan0", "list_networks").Output()
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

func (a *Ap) ConnectToNetwork(ssid, key string) error {

	net, err := a.EnsureWpaNetworkAdded()
	if err != nil {
		return err
	}

	response, err := commands.Run("wpa_cli", "-i", "wlan0", "set_network", net, "ssid", "\""+ssid+"\"")
	if err != nil {
		return err
	}
	logrus.Infof("set_network ssid: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "set_network", net, "psk", "\""+key+"\"")
	if err != nil {
		return err
	}
	logrus.Infof("set_network psk: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "set_network", net, "key_mgmt", "WPA-PSK")
	if err != nil {
		return err
	}
	logrus.Infof("set_network key_mgmt: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "set_network", net, "priority", "10")
	if err != nil {
		return err
	}
	logrus.Infof("set_network priority: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "enable_network", net)
	if err != nil {
		return err
	}
	logrus.Infof("enable_network: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "save_config", net)
	if err != nil {
		return err
	}
	logrus.Infof("save_config: %s", response)

	response, err = commands.Run("wpa_cli", "-i", "wlan0", "reconfigure", net)
	if err != nil {
		return err
	}
	logrus.Infof("reconfigure: %s", response)

	return fmt.Errorf("could not connect to wifi")
}

type WpaNetwork struct {
	Bssid       string `json:"bssid"`
	Frequency   string `json:"frequency"`
	SignalLevel string `json:"signalLevel"`
	Flags       string `json:"flags"`
	Ssid        string `json:"ssid"`
}

func (a *Ap) ScanNetworks() ([]*WpaNetwork, error) {

	wpaNetworks := []*WpaNetwork{}

	scanOut, err := commands.Run("wpa_cli", "-i", "wlan0", "scan")
	if err != nil {
		return wpaNetworks, err
	}

	if scanOut != "OK" {
		return wpaNetworks, fmt.Errorf("expected OK from wpa_cli scan got: %s", scanOut)
	}
	time.Sleep(1 * time.Second)
	networkListOut, err := commands.Run("wpa_cli", "-i", "wlan0", "scan_results")
	if err != nil {
		return wpaNetworks, err
	}

	tmp := strings.Split(string(networkListOut), "\n")
	for _, netRecord := range tmp[1:] {
		if strings.Contains(netRecord, "[P2P]") {
			continue
		}

		fields := strings.Fields(netRecord)

		if len(fields) > 4 {
			fmt.Println("networkListOut", fields)
			fmt.Println("len", len(fields))
			ssid := strings.Join(fields[4:], " ")
			wpaNetworks = append(wpaNetworks, &WpaNetwork{
				Bssid:       fields[0],
				Frequency:   fields[1],
				SignalLevel: fields[2],
				Flags:       fields[3],
				Ssid:        ssid,
			})
		}
	}

	return wpaNetworks, nil
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
