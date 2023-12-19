package webserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	Port string
	ap   *ap.Ap
}

func New(port string, ap *ap.Ap) *Webserver {
	return &Webserver{
		Port: port,
		ap:   ap,
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
	router.GET("/api/current-ssid-v1", func(c *gin.Context) {
		ssid, err := ws.ap.GetConnectedSSID()
		if err != nil {
			logrus.Error(err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "error fetching ssid",
			})
		}
		c.JSON(http.StatusOK, gin.H{"ssid": ssid})
	})
	router.POST("/api/connect-v1", err(ws.connect))

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
