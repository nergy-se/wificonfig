package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nergy-se/wificonfig/pkg/ap"
	"github.com/nergy-se/wificonfig/pkg/commands"
	"github.com/nergy-se/wificonfig/pkg/webserver"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

type App struct {
	webserver *webserver.Webserver
	ap        *ap.Ap

	AliveURL                  string
	EthernetInterfaceName     string
	Interval                  time.Duration
	IP                        string
	wpaSupplicantConfigFile   string
	apssid                    string
	appsk                     string
	wiredStaticConfigLocation string

	// apDelay is how long we stay without a connection before we let wpa_supplicant fall back to our AP.
	apDelay time.Duration
	// apRetryInterval is how often we scan for the station network while in AP mode.
	apRetryInterval time.Duration
	// lastStation is when we were last connected (ethernet or wifi), or started connecting to a station network.
	lastStation time.Time
	// nextAPScan is when we next look for the station network while in AP mode.
	nextAPScan time.Time
	// stationFailures counts how many times in a row we left AP mode for a station network without connecting to it.
	stationFailures int
	// handledManualConnect is the last setup page submit reconcile knows of. It is passed to
	// LeaveAP and SetAPNetworkEnabled, which skip if a newer submit happened meanwhile.
	handledManualConnect time.Time
}

func NewApp(c *cli.Context, ws *webserver.Webserver, ap *ap.Ap) *App {
	return &App{
		webserver:                 ws,
		ap:                        ap,
		AliveURL:                  c.String("alive-url"),
		Interval:                  c.Duration("check-interval"),
		EthernetInterfaceName:     c.String("ethernet-interface"),
		IP:                        c.String("ap-ip"),
		apssid:                    c.String("ap-ssid"),
		appsk:                     c.String("ap-psk"),
		wpaSupplicantConfigFile:   c.String("wpa-supplicant-config"),
		wiredStaticConfigLocation: c.String("wired-static-config-location"),
		apDelay:                   c.Duration("ap-delay"),
		apRetryInterval:           c.Duration("ap-retry-interval"),
	}
}

func (a *App) Start(ctx context.Context) error {
	if a.apssid == "" {
		return fmt.Errorf("missing config ap-ssid")
	}
	if a.appsk == "" {
		return fmt.Errorf("missing config ap-psk")
	}

	err := a.ensureWpaConfig()
	if err != nil {
		return err
	}

	a.lastStation = time.Now() // give a configured station network apDelay to connect after boot
	go a.tickerLoop(ctx, a.Interval)

	a.webserver.Start(ctx)

	return nil
}

func (a *App) ensureWpaConfig() error {
	_, err := os.Stat(a.wpaSupplicantConfigFile)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(a.wpaSupplicantConfigFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, fmt.Sprintf(`ctrl_interface=/var/run/wpa_supplicant
ctrl_interface_group=0
update_config=1
country=SE
ap_scan=1

network={
	ssid="%s"
	psk="%s"
	key_mgmt=WPA-PSK
	mode=2
	frequency=2437
}
`, a.apssid, a.appsk))
		return err

	}
	return nil
}

func (a *App) tickerLoop(ctx context.Context, d time.Duration) {

	ticker := time.NewTicker(d)
	logrus.Infof("Config OK. starting check loop for %s", d)
	err := a.reconcile(ctx)
	if err != nil {
		logrus.Error(err)
	}
	for {
		select {
		case <-ticker.C:
			err := a.reconcile(ctx)
			if err != nil {
				logrus.Error(err)
			}

		case <-ctx.Done():
			ticker.Stop()
			return
		}
	}
}

func (a *App) syncStaticConfigIfNeeded() error {
	if strings.HasPrefix(a.wiredStaticConfigLocation, "/etc/systemd/network") {
		return nil // we already have config in correct location no need to sync it to /etc/systemd/network
	}

	dstFn := filepath.Join("/etc/systemd/network", filepath.Base(a.wiredStaticConfigLocation))

	srcHash, err := hash(a.wiredStaticConfigLocation)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // if the config files does not exist we should remove the destination
			err := os.Remove(dstFn)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // file is already removed
				}
				return err
			}

			_, err = commands.Run("networkctl", "reload")
			return err
		}
		return err
	}

	dstHash, err := hash(dstFn)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if dstHash == srcHash {
		return nil // nothing to do files are the same.
	}

	srcFile, err := os.Open(a.wiredStaticConfigLocation)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstFn, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}

	_, err = commands.Run("networkctl", "reload")
	return err
}
func (a *App) reconcile(ctx context.Context) error {
	// A network submitted from the setup page (for example a corrected PSK, maybe over ethernet)
	// is a fresh start, so earlier failed attempts should not make the next retry wait an hour.
	if manual := a.ap.LastManualConnect(); manual.After(a.handledManualConnect) {
		a.handledManualConnect = manual
		a.stationFailures = 0
		a.nextAPScan = time.Time{}
	}

	alive, err := a.checkAlive()
	if err != nil {
		logrus.Error(fmt.Errorf("error checking alive: %w", err))
	}

	err = a.syncStaticConfigIfNeeded()
	if err != nil {
		logrus.Warn(err)
	}

	activeInt, _, err := GetActiveInterface()
	if err != nil {
		return err
	}

	if alive && activeInt != nil && activeInt.Name == a.EthernetInterfaceName { // ethernet connected and alive
		a.lastStation = time.Now() // if ethernet goes away give the wifi apDelay to connect
		err := a.ap.StopDnsmasq()
		if err != nil {
			return err
		}
		err = a.ap.StopWpaSupplicant()
		if err != nil {
			return err
		}

		return nil
	}

	// no ethernet connection detected so lets make sure wpa_supplicant is running
	started, err := a.ap.StartWpaSupplicant(ctx)
	if err != nil {
		return err
	}
	if started {
		a.disableAPAtStart()
	}

	isConnectedWifi, err := a.ap.WpaConnectedToWifi()
	if err != nil {
		return err
	}

	if isConnectedWifi { // we are connectd to wifi station.
		a.lastStation = time.Now()
		a.stationFailures = 0
		a.nextAPScan = time.Time{} // look for the station network at once if we end up as AP again
		err := a.ap.StopDnsmasq()
		if err != nil {
			return err
		}

		if int, _, err := InterfaceHasIP(net.ParseIP(a.IP)); int != nil && err == nil && int.Name == "wlan0" { // if we have our AP ip lets restart the network to get DHCP.
			_, err = commands.Run("networkctl", "reconfigure", "wlan0")
			if err != nil {
				return err
			}
		}

		// Without this a short drop (router reboot) makes wpa_supplicant switch to our AP after a single scan.
		// After the reconfigure above, so an error here does not leave us without DHCP.
		return a.ap.SetAPNetworkEnabled(false, a.handledManualConnect)
	}

	// no wifi or ethernet lets be AP and DHCP

	isAP, err := a.ap.WpaIsAp()

	if err != nil {
		return err
	}

	if isAP {
		stations, stationsErr := a.ap.StationNetworks()
		// After boot or a restart of wpa_supplicant the AP network is enabled until disableAPAtStart
		// or our first reconcile disables it, so the AP may have started before apDelay.
		// Leave it before setting up dnsmasq and the AP IP only to tear them down again.
		// Not right after a network was submitted from the setup page though: if it failed (wrong PSK)
		// the AP is how the user corrects it, also on a new device within apDelay of boot.
		now := time.Now()
		if stationsErr == nil && !a.wantAP(len(stations), now) && !a.recentManualConnect(now) {
			logrus.Info("AP started before ap-delay, leaving AP mode to keep looking for station network")
			_, err := a.ap.LeaveAP(a.handledManualConnect)
			return err
		}

		err = a.ap.StartDnsmasq(ctx)
		if err != nil {
			return err
		}
		_, err = commands.Run("ifconfig", "wlan0", a.IP)
		if err != nil {
			return err
		}
		// returned after the AP setup above, so an error here does not leave setup clients without DHCP.
		if stationsErr != nil {
			return stationsErr
		}
		return a.retryStation(stations)
	}

	stations, err := a.ap.StationNetworks()
	if err != nil {
		return err
	}
	now := time.Now()
	if a.recentManualConnect(now) {
		// Leave the AP network as ConnectToNetwork set it. Right after its reconfigure we are
		// neither connected nor an AP while associating; disabling the AP then would stop a
		// wrong PSK from falling back to it, also on a new device within apDelay of boot.
		return nil
	}
	want := a.wantAP(len(stations), now)
	if want && len(stations) > 0 && now.After(a.nextAPScan) {
		// Give the AP at least apRetryInterval before the first retry scan. Otherwise, when the
		// station network is visible but connecting fails (changed PSK), we would leave the AP
		// at the next check, before anyone could join it to fix the PSK.
		a.nextAPScan = now.Add(a.apRetryInterval)
	}
	return a.ap.SetAPNetworkEnabled(want, a.handledManualConnect)
}

// recentManualConnect reports if a network was submitted from the setup page within
// apRetryInterval or apDelay, whichever is longer, since both the retry scan and the
// early leave (which runs within apDelay) check it.
func (a *App) recentManualConnect(now time.Time) bool {
	return now.Sub(a.ap.LastManualConnect()) < max(a.apRetryInterval, a.apDelay)
}

// disableAPAtStart disables the AP network in a just started wpa_supplicant, which reads it
// enabled from the config file, before its first scan can make it fall back to the AP.
// If we lose that race the early leave in reconcile's AP branch leaves the AP at the next check.
func (a *App) disableAPAtStart() {
	err := a.ap.WaitForWpa(5 * time.Second)
	if err != nil {
		logrus.Warn(err)
		return
	}
	stations, err := a.ap.StationNetworks()
	if err != nil {
		logrus.Warn(err)
		return
	}
	if !a.wantAP(len(stations), time.Now()) {
		err = a.ap.SetAPNetworkEnabled(false, a.handledManualConnect)
		if err != nil {
			logrus.Warn(err)
		}
	}
}

// wantAP reports if wpa_supplicant may fall back to our AP. Without a station network
// the AP is the only way to configure one, otherwise we wait apDelay for it to come back.
func (a *App) wantAP(stations int, now time.Time) bool {
	return stations == 0 || now.Sub(a.lastStation) > a.apDelay
}

// retryStation leaves AP mode when a configured station network is visible again.
// wpa_supplicant does not scan for station networks itself while it is an AP,
// so without this we would stay an AP until someone connects to it and reconfigures the wifi.
func (a *App) retryStation(stations []ap.WpaListedNetwork) error {
	if len(stations) == 0 {
		return nil
	}
	now := time.Now()
	if now.Before(a.nextAPScan) {
		return nil
	}
	// A network was just submitted from the setup page. If it failed (wrong PSK) we are an AP
	// again; stay one so the user can correct it instead of losing the AP for apDelay.
	if a.recentManualConnect(now) {
		return nil
	}

	a.nextAPScan = now.Add(a.apRetryInterval)

	ssids := make([]string, len(stations))
	for i, s := range stations {
		ssids[i] = s.Ssid
	}
	ssid, err := a.ap.VisibleSSID(ssids)
	if err != nil {
		return fmt.Errorf("error scanning for station network: %w", err)
	}
	if ssid == "" {
		return nil
	}

	// LeaveAP skips if a network was submitted from the setup page meanwhile, for example while
	// we waited for the scan, since that submit needs the AP to fall back to if it fails.
	left, err := a.ap.LeaveAP(a.handledManualConnect)
	if err != nil || !left {
		return err
	}
	logrus.Infof("station network %s is visible, left AP mode", ssid)
	// Only counted once we really left, so wpa_cli errors do not push the next attempt out.
	a.lastStation = now // if connecting fails wantAP enables the AP again after apDelay
	// If it keeps failing (wrong or changed PSK) back off, so the AP stays up long enough to fix it.
	// Counted from when the AP is back, so after each failure it stays up 10, 20, 40, then 60 min
	// (the first window, before any failure, is apRetryInterval).
	a.stationFailures++
	a.nextAPScan = now.Add(a.apDelay + a.stationRetryDelay())
	return nil
}

// stationRetryDelay doubles apRetryInterval for each failed attempt, up to an hour.
func (a *App) stationRetryDelay() time.Duration {
	maxDelay := max(time.Hour, a.apRetryInterval)
	d := a.apRetryInterval
	for i := 0; i < a.stationFailures && d < maxDelay; i++ {
		d *= 2
	}
	return min(d, maxDelay)
}

func GetActiveInterface() (*net.Interface, net.IP, error) {
	outboundIP, err := GetOutboundIP()
	if err != nil {
		return nil, nil, nil // we ignore if we get for example connect: network is unreachable
	}
	return InterfaceHasIP(outboundIP)
}

func InterfaceHasIP(expectedIP net.IP) (*net.Interface, net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, i := range interfaces {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, nil, err
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				return nil, nil, err
			}
			if ip.Equal(expectedIP) {
				return &i, ip, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("found no interface")

}

func GetOutboundIP() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP, nil
}

var aliveClient = &http.Client{
	Timeout: time.Second * 10,
}

func (a *App) checkAlive() (bool, error) {
	r, err := aliveClient.Get(a.AliveURL)
	if err != nil {
		return false, err
	}

	return r.StatusCode == 200, nil
}

func hash(r string) (string, error) {
	h := sha256.New()

	f, err := os.Open(r)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return sum, nil
}
