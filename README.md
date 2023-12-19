# wificonfig

Handles initial wifi setup through a web gui and local AP

* If ethernet are connected we stop wifi.
* If we dont reach --alive-url when we will start local AP for setup.
* If wifi is not connected or cannot connect we start local AP for setup.

Supports "capitative portal" when connecting to AP for setup your phone will go to configure wifi page automatically.

Tested on raspberry pi 4.

## running

```
NAME:
   wificonfig - wificonfig, auto accesspoint if not connected to internet

USAGE:
   wificonfig [global options] command [command options] [arguments...]

VERSION:
   Version: "dev", BuildTime: "", Commit: ""

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --log-level value              available levels are: panic,fatal,error,warning,info,debug,trace (default: "info")
   --alive-url value              url to check if we should
   --listen-port value            webserver listen port (default: "8080")
   --wpa-supplicant-config value  wpa_supplicant config location (default: "/etc/wpa_supplicant.conf")
   --ap-ip value                  default ip when in AP mode (default: "192.168.27.1")
   --ap-ssid value                ssid of the AP
   --ap-psk value                 password of the AP
   --dhcp-start value             dhcp start address (default: "192.168.27.100")
   --dhcp-end value               dhcp end address (default: "192.168.27.150")
   --ethernet-interface value     ethernet interface name (default: "end0")
   --check-interval value         check interval (default: 30s)
   --help, -h                     show help
   --version, -v                  print the version
```
