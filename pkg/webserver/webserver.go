package webserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"text/template"
	"time"

	"github.com/fortnoxab/ginprometheus"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/jonaz/ginlogrus"
	"github.com/nergy-se/wificonfig/pkg/ap"
	"github.com/sirupsen/logrus"
)

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

	// router.GET("/", func(c *gin.Context) {
	// 	fmt.Fprintf(c.Writer, `<a href="/machines">Machines</a>`)
	// })
	router.GET("/", err(ws.index))
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

	fmt.Println("networks is", networks)

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

func (ws *Webserver) index(c *gin.Context) error {
	t := `<!DOCTYPE html>
<html lang="en">
<head>
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin:25px" onload="checkConnected()">
<script>
const checkConnected = async () => {
  try {
    const response = await fetch('/api/current-ssid-v1');
    const data = await response.json();
	console.log("data", data);

	if ( response.status != 200){
	document.getElementById("error").innerHTML = "Error: "+ data.error;
		return;
	}
	document.getElementById("error").innerHTML = "";

	if(data.ssid !== ""){
	document.getElementById("h1").innerHTML = "Connected to: "+data.ssid;
	}
  } catch (error) {
    console.error(error);
  }
}
const connect = () =>  {

	const ssid = document.getElementById('ssid').value;
	const psk = document.getElementById('psk').value;
    let options = {
        method: "POST",
        headers: {
            "Content-Type":"application/json",
        },
		body: JSON.stringify({ssid: ssid, psk: psk})      
    }
    fetch("/api/connect-v1", options);
}
const scan = async () => {
  try {
	document.getElementById("data").innerHTML = '<tr><td colspan="4">Scanning now...</td></tr>';
    const response = await fetch('/api/scan-v1');
    const data = await response.json();
	console.log("data", data);
	var temp = "";

	if ( response.status != 200){
	document.getElementById("error").innerHTML = "Error: "+ data.error;
		return;
	}
	document.getElementById("error").innerHTML = "";

	data.forEach((x) => {
		temp += "<tr>";
		temp += "<td>" + x.ssid + "</td>";
		temp += "<td>" + x.frequency + "</td>";
		temp += "<td>" + x.signalLevel + "</td>";
		temp += "<td><button onclick=\"event.preventDefault();document.getElementById('ssid').value='"+x.ssid+"';\";>Connect</button></td>";
		temp += "</tr>"
	});

	document.getElementById("data").innerHTML = temp;
  } catch (error) {
    console.error(error);
  }
}
</script>
<h2 id="h1">Connect to wifi</h2>
<form method="post" action="/test" id="myForm">
  <label for="ssid">SSID:</label><br>
  <input type="text" id="ssid" name="ssid"><br>
  <label for="psk">Password:</label><br>
  <input type="text" id="psk" name="psk"><br><br>
  <input value="Connect" type="submit" onclick="event.preventDefault();connect();">
</form>
<button style="margin-top:20px;" onclick="event.preventDefault();scan();">Scan for networks</button>
<div style="padding-top:10px;" >
    <table class="table" border="0">
        <thead>
            <tr>
                <th>SSID</th>
                <th>Freq</th>
                <th>Signal</th>
                <th>Connect</th>
            </tr>
        </thead>
        <tbody id="data"><tr><td colspan="4">Not scanned yet</td></tr></tbody>
    </table>
</div>
	<h2 id="error" style="color:red"></h2>
</body>
</html>`

	tmpl, err := template.New("index").Parse(t)
	if err != nil {
		return err
	}

	type host struct {
		Name       string `json:"name"`
		IP         string `json:"ip"`
		Online     bool
		Accepted   bool
		Git        bool
		LastUpdate time.Time
	}
	hostList := make(map[string]*host)

	return tmpl.Execute(c.Writer, hostList)
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
