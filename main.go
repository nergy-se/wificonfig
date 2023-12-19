package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nergy-se/wificonfig/pkg/ap"
	"github.com/nergy-se/wificonfig/pkg/webserver"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var (
	// TODO set on build
	Version     = "dev"
	BuildTime   = ""
	BuildCommit = ""
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM)
	defer stop()
	err := app().RunContext(ctx, os.Args)
	if err != nil {
		logrus.Error(err)
		os.Exit(1)
	}
}

func app() *cli.App {
	app := cli.NewApp()
	app.Name = "wificonfig"
	app.Usage = "wificonfig, auto accesspoint if not connected to internet"
	app.Version = fmt.Sprintf(`Version: "%s", BuildTime: "%s", Commit: "%s"`, Version, BuildTime, BuildCommit)
	app.Before = globalBefore
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "log-level",
			Value: "info",
			Usage: "available levels are: " + strings.Join(getLevels(), ","),
		},
		&cli.StringFlag{
			Name:  "alive-url",
			Usage: "url to check if we should",
		},
		&cli.StringFlag{
			Name:  "listen-port",
			Value: "8080",
			Usage: "webserver listen port",
		},
		&cli.StringFlag{
			Name:  "wpa-supplicant-config",
			Value: "/etc/wpa_supplicant.conf",
			Usage: "wpa_supplicant config location",
		},
		&cli.StringFlag{
			Name:  "ap-ip",
			Value: "192.168.27.1",
			Usage: "default ip when in AP mode",
		},
		&cli.StringFlag{
			Name:  "ap-ssid",
			Value: "",
			Usage: "ssid of the AP",
		},
		&cli.StringFlag{
			Name:  "ap-psk",
			Value: "",
			Usage: "password of the AP",
		},
		&cli.StringFlag{
			Name:  "dhcp-start",
			Value: "192.168.27.100",
			Usage: "dhcp start address",
		},
		&cli.StringFlag{
			Name:  "dhcp-end",
			Value: "192.168.27.150",
			Usage: "dhcp end address",
		},
		&cli.StringFlag{
			Name:  "ethernet-interface",
			Value: "end0",
			Usage: "ethernet interface name",
		},
		&cli.DurationFlag{
			Name:  "check-interval",
			Value: time.Second * 30,
			Usage: "check interval",
		},
	}

	app.Action = func(c *cli.Context) error {
		ap := ap.New(c)
		ws := webserver.New(c.String("listen-port"), ap)
		app := NewApp(c, ws, ap)
		return app.Start(c.Context)
	}

	return app
}

func globalBefore(c *cli.Context) error {
	logrus.SetFormatter(&logrus.TextFormatter{TimestampFormat: time.RFC3339Nano, FullTimestamp: true})
	lvl, err := logrus.ParseLevel(c.String("log-level"))
	if err != nil {
		return err
	}
	if lvl != logrus.InfoLevel {
		fmt.Fprintf(os.Stderr, "using loglevel: %s\n", lvl.String())
	}
	logrus.SetLevel(lvl)
	return nil
}
func getLevels() []string {
	lvls := make([]string, len(logrus.AllLevels))
	for k, v := range logrus.AllLevels {
		lvls[k] = v.String()
	}
	return lvls
}

// OK conditions
// Are we connected to ethernet and working internet
// Are we connected to wifi in client mode?
// Otherwise start AP mode

// TODO IN AP MODE: Set local static ip 192.168.27.1
// ifconfig wlan0 192.168.27.1

// kör alltid wpa_supplicant i foreground
// wpa_supplicant -c/etc/wpa_supplicant.conf -iwlan0 -Dnl80211
// den konfas då med wpa_cli -p <control sockets location>

// wpa config docs https://w1.fi/cgit/hostap/plain/wpa_supplicant/wpa_supplicant.conf
// ap_scan=1 betyder skapa AP om vi inte är anslutna till något.

//TODO konfa dhcp wlan0
/*
/etc/systemd/network/25-wlan.network
[Match]
Name=wlan0

[Network]
DHCP=ipv4
*/
