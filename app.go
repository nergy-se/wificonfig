package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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

	AliveURL                string
	EthernetInterfaceName   string
	Interval                time.Duration
	IP                      string
	wpaSupplicantConfigFile string
	apssid                  string
	appsk                   string
}

func NewApp(c *cli.Context, ws *webserver.Webserver, ap *ap.Ap) *App {
	return &App{
		webserver:               ws,
		ap:                      ap,
		AliveURL:                c.String("alive-url"),
		Interval:                c.Duration("check-interval"),
		EthernetInterfaceName:   c.String("ethernet-interface"),
		IP:                      c.String("ap-ip"),
		apssid:                  c.String("ap-ssid"),
		appsk:                   c.String("ap-psk"),
		wpaSupplicantConfigFile: c.String("wpa-supplicant-config"),
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
func (a *App) reconcile(ctx context.Context) error {
	alive, err := a.checkAlive()
	if err != nil {
		logrus.Error(fmt.Errorf("error checking alive: %w", err))
	}

	intName, _, err := GetActiveInterface()
	if err != nil {
		return err
	}

	if alive && intName == a.EthernetInterfaceName { // ethernet connected and alive
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
	err = a.ap.StartWpaSupplicant(ctx)
	if err != nil {
		return err
	}

	isConnectedWifi, err := a.ap.WpaConnectedToWifi()
	if err != nil {
		return err
	}

	if isConnectedWifi { // we are connectd to wifi station.
		err := a.ap.StopDnsmasq()
		if err != nil {
			return err
		}

		if alive {
			return nil
		}

		if hasIP, _, err := InterfaceHasIP(net.ParseIP(a.IP)); hasIP != "" && err == nil { // if we have our AP ip lets restart the network to get DHCP.
			logrus.Debug("networkctl reconfigure wlan0")
			_, err = commands.Run("networkctl", "reconfigure", "wlan0")
			return err
		}
		return nil
	}

	// no wifi or ethernet lets be AP and DHCP

	isAP, err := a.ap.WpaIsAp()

	if err != nil {
		return err
	}

	if isAP {
		err = a.ap.StartDnsmasq(ctx)
		if err != nil {
			return err
		}
		logrus.Debugf("ifconfig wlan0 %s", a.IP)
		_, err = commands.Run("ifconfig", "wlan0", a.IP)
		return err
	}

	return nil
}

func GetActiveInterface() (string, net.IP, error) {
	outboundIP, err := GetOutboundIP()
	if err != nil {
		return "", nil, nil // we ignore if we get for example connect: network is unreachable
	}
	return InterfaceHasIP(outboundIP)
}

func InterfaceHasIP(expectedIP net.IP) (string, net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", nil, err
	}
	for _, i := range interfaces {
		addrs, err := i.Addrs()
		if err != nil {
			return "", nil, err
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				return "", nil, err
			}
			if ip.Equal(expectedIP) {
				return i.Name, ip, nil
			}
		}
	}
	return "", nil, fmt.Errorf("found no interface")

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
	Timeout: time.Second * 1,
}

func (a *App) checkAlive() (bool, error) {
	r, err := aliveClient.Get(a.AliveURL)
	if err != nil {
		return false, err
	}

	return r.StatusCode == 200, nil
}
