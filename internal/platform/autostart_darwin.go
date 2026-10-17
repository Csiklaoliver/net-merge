package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const agentLabel = "io.github.rohit267.net-merge"

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

// Autostart turns start-at-sign-in "on" or "off", or prints its "status". It is a LaunchAgent
// running `sudo -n net-merge tray`, which only works once `sudo net-merge install` has added the
// passwordless rule; otherwise sign-in would need a password the agent can't type.
func Autostart(action string) error {
	if os.Geteuid() == 0 {
		return errors.New("run this as your normal user, not with sudo")
	}
	path, err := agentPath()
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_, statErr := os.Stat(path)
	switch action {
	case "status":
		if statErr == nil {
			fmt.Println("autostart is ON (" + path + ").")
		} else {
			fmt.Println("autostart is OFF.")
		}
		return nil
	case "off":
		exec.Command("/bin/launchctl", "bootout", domain+"/"+agentLabel).Run() // not loaded is fine
		if statErr != nil {
			fmt.Println("autostart is already OFF.")
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Println("autostart is OFF.")
		return nil
	}
	if exec.Command("/usr/bin/sudo", "-n", "-l", InstalledExe(), "tray").Run() != nil {
		return errors.New("the tray can't start at sign-in without the passwordless rule; run once: sudo net-merge install")
	}
	home, _ := os.UserHomeDir()
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + agentLabel + `</string>
<key>ProgramArguments</key><array><string>/usr/bin/sudo</string><string>-n</string><string>` + InstalledExe() + `</string><string>tray</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>30</integer>
<key>StandardOutPath</key><string>` + filepath.Join(home, "Library", "Logs", "net-merge.log") + `</string>
<key>StandardErrorPath</key><string>` + filepath.Join(home, "Library", "Logs", "net-merge.log") + `</string>
</dict></plist>
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	exec.Command("/bin/launchctl", "bootout", domain+"/"+agentLabel).Run()
	if out, err := exec.Command("/bin/launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
	}
	fmt.Println("autostart is ON: net-merge starts at sign-in (" + path + ").")
	return nil
}
