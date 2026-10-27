
build: export CGO_ENABLED=0
build:
	GOOS=linux GOARCH=amd64 go build -o wificonfig-linux-amd64 .
	GOOS=linux GOARCH=arm GOARM=7 go build -o wificonfig-linux-arm .
	GOOS=linux GOARCH=arm64 go build -o wificonfig-linux-arm64 .

test:
	go test ./...
