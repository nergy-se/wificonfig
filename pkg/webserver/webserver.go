package webserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/fortnoxab/ginprometheus"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/jonaz/ginlogrus"
	"github.com/nergy-se/wificonfig/pkg/ap"
	"github.com/sirupsen/logrus"

	_ "embed"
)

//go:embed index.html
var index []byte

type Webserver struct {
	Port                      string
	ap                        *ap.Ap
	wiredStaticConfigLocation string
}

func New(port string, ap *ap.Ap, wiredStaticConfigLocation string) *Webserver {
	return &Webserver{
		Port:                      port,
		ap:                        ap,
		wiredStaticConfigLocation: wiredStaticConfigLocation,
	}
}

func (ws *Webserver) Init() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	p := ginprometheus.New("http")
	p.Use(router)

	logIgnorePaths := []string{
		"/health",
		"/metrics",
		"favicon.ico",
	}
	router.Use(ginlogrus.New(logrus.StandardLogger(), logIgnorePaths...), gin.Recovery())

	router.GET("/", func(c *gin.Context) {
		c.Writer.Header().Set("location", "/")
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
		c.Status(http.StatusFound)
	})
	router.GET("/generate_204", func(c *gin.Context) {
		c.Writer.Header().Set("location", "/")
		c.Status(http.StatusFound)
	})
	router.GET("/api/scan-v1", err(ws.scan))
	router.GET("/api/status-v1", func(c *gin.Context) {

		interfaces, err := net.Interfaces()
		if err != nil {
			return
		}

		type Interface struct {
			Name     string   `json:"name"`
			Ethernet bool     `json:"ethernet"`
			Static   bool     `json:"static"`
			IPs      []string `json:"ips"`
		}

		list := []*Interface{}

		for _, i := range interfaces {
			if i.Flags&net.FlagLoopback != 0 {
				continue // skip loopback
			}

			iface := &Interface{Name: i.Name, Ethernet: i.Name == ws.ap.EthernetInterfaceName}
			if i.Name == ws.ap.EthernetInterfaceName {
				iface.Ethernet = true
				_, err := os.Stat(ws.wiredStaticConfigLocation)
				if err == nil {
					iface.Static = true
				}
			}
			addrs, err := i.Addrs()
			if err != nil {
				return
			}

			for _, ad := range addrs {
				a, _, _ := net.ParseCIDR(ad.String())
				if a.To4() == nil {
					continue
				}
				iface.IPs = append(iface.IPs, ad.String())
			}
			list = append(list, iface)
		}
		ssid, err := ws.ap.GetConnectedSSID()
		if err != nil {
			logrus.Error(err)
		}

		c.JSON(http.StatusOK, gin.H{
			"ssid":       ssid,
			"interfaces": list,
		})
	})
	router.POST("/api/connect-v1", err(ws.connect))
	router.POST("/api/ethernet-v1", err(ws.configureEthernetIP))

	pprof.Register(router)
	return router
}

func (ws *Webserver) scan(c *gin.Context) error {
	networks, err := ws.ap.ScanNetworks()
	if err != nil {
		logrus.Error(err)
		return fmt.Errorf("scanning for network failed")
	}

	c.JSON(http.StatusOK, networks)
	return nil
}
func (ws *Webserver) configureEthernetIP(c *gin.Context) error {
	type respStruct struct {
		IP string
	}
	resp := &respStruct{}
	err := c.BindJSON(resp)
	if err != nil {
		return err
	}

	err = ws.ap.EnsureEthernetStaticIP(resp.IP)

	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second)
	c.JSON(http.StatusOK, gin.H{})
	return nil
}

func (ws *Webserver) connect(c *gin.Context) error {
	type respStruct struct {
		SSID string
		PSK  string
	}
	resp := &respStruct{}
	err := c.BindJSON(resp)
	if err != nil {
		return err
	}

	err = ws.ap.ConnectToNetwork(resp.SSID, resp.PSK)
	if err != nil {
		logrus.Error(err)
		return fmt.Errorf("failed to connect")
	}

	c.JSON(http.StatusOK, gin.H{})
	return nil
}

func (ws *Webserver) Start(ctx context.Context) {
	srv := &http.Server{
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		Addr:              ":" + ws.Port,
		Handler:           ws.Init(),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logrus.Fatalf("error starting webserver %s", err)
		}
	}()

	logrus.Debug("webserver started")

	<-ctx.Done()

	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctxShutDown); !errors.Is(err, http.ErrServerClosed) && err != nil {
		logrus.Error(err)
	}
}

func err(f func(c *gin.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := f(c)
		if err != nil {
			logrus.Error(err)
			// TODO handle error messages from 400
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
			})
			// c.AbortWithStatus(http.StatusInternalServerError)
		}
	}
}
