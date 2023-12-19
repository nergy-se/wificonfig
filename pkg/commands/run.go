package commands

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

func Run(head string, parts ...string) (string, error) {
	var err error

	logrus.Debug(append([]string{"running: " + head}, parts...))
	cmd := exec.Command(head, parts...) // #nosec
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("run: %s %s error: %w stderr: %s stdout: %s", head, strings.Join(parts, " "), err, stderr.String(), stdout.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
