package kutil

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"text/template"
)

const (
	initSystemV = initFlavor(iota)
	initUpstart
	initSystemd
)

type LinuxService struct {
	flavor                         initFlavor
	name, displayName, description string
	exePath                        string
}

type initFlavor uint8

func getFlavor() initFlavor {
	flavor := initSystemV
	if isUpstart() {
		flavor = initUpstart
	}
	if isSystemd() {
		flavor = initSystemd
	}
	return flavor
}

func newService(name, displayName, description, exePath string) (*LinuxService, error) {
	s := &LinuxService{
		flavor:      getFlavor(),
		name:        name,
		displayName: displayName,
		description: description,
		exePath:     exePath,
	}

	return s, nil
}

func isUpstart() bool {
	if _, err := os.Stat("/sbin/initctl"); err == nil {
		return true
	}
	return false
}

func isSystemd() bool {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return true
	}
	return false
}

func (f initFlavor) String() string {
	switch f {
	case initSystemV:
		return "System-V"
	case initUpstart:
		return "Upstart"
	case initSystemd:
		return "systemd"
	default:
		return "unknown"
	}
}

func (f initFlavor) ConfigPath(name string) string {
	switch f {
	case initSystemV:
		return "/etc/init.d/" + name
	case initUpstart:
		return "/etc/init/" + name + ".conf"
	case initSystemd:
		return "/etc/systemd/system/" + name + ".service"
	default:
		return ""
	}
}

func (f initFlavor) GetTemplate() *template.Template {
	var templ string
	switch f {
	case initSystemV:
		templ = systemVScript
	case initUpstart:
		templ = upstartScript
	case initSystemd:
		templ = systemdScript
	}
	return template.Must(template.New(f.String() + "Script").Parse(templ))
}

func (s *LinuxService) Install() error {
	confPath := s.flavor.ConfigPath(s.name)
	_, err := os.Stat(confPath)
	if err == nil {
		return fmt.Errorf("Init already exists: %s", confPath)
	}

	f, err := os.Create(confPath)
	if err != nil {
		return err
	}
	defer f.Close()

	path := s.exePath

	var to = &struct {
		Display     string
		Description string
		Path        string
	}{
		s.displayName,
		s.description,
		path,
	}

	err = s.flavor.GetTemplate().Execute(f, to)
	if err != nil {
		return err
	}

	if s.flavor == initSystemV {
		if err = os.Chmod(confPath, 0755); err != nil {
			return err
		}
		for _, i := range [...]string{"2", "3", "4", "5"} {
			if err = os.Symlink(confPath, "/etc/rc"+i+".d/S50"+s.name); err != nil {
				continue
			}
		}
		for _, i := range [...]string{"0", "1", "6"} {
			if err = os.Symlink(confPath, "/etc/rc"+i+".d/K02"+s.name); err != nil {
				continue
			}
		}
	}

	if s.flavor == initSystemd {
		return exec.Command("systemctl", "daemon-reload").Run()
	}

	return nil
}

func (s *LinuxService) Remove() error {
	if s.flavor == initSystemd {
		exec.Command("systemctl", "disable", s.name+".service").Run()
	}
	if err := os.Remove(s.flavor.ConfigPath(s.name)); err != nil {
		return err
	}
	return nil
}

func (s *LinuxService) Run(onStart, onStop func() error) (err error) {
	err = onStart()
	if err != nil {
		return err
	}
	defer func() {
		err = onStop()
	}()

	sigChan := make(chan os.Signal, 3)

	signal.Notify(sigChan, os.Interrupt, os.Kill)

	<-sigChan

	return nil
}

func (s *LinuxService) Start() error {
	if s.flavor == initUpstart {
		return exec.Command("initctl", "start", s.name).Run()
	}
	if s.flavor == initSystemd {
		return exec.Command("systemctl", "start", s.name+".service").Run()
	}
	return exec.Command("service", s.name, "start").Run()
}

func (s *LinuxService) Stop() error {
	if s.flavor == initUpstart {
		return exec.Command("initctl", "stop", s.name).Run()
	}
	if s.flavor == initSystemd {
		return exec.Command("systemctl", "stop", s.name+".service").Run()
	}
	return exec.Command("service", s.name, "stop").Run()
}

func (s *LinuxService) Error(format string, a ...interface{}) error {
	return nil
}
func (s *LinuxService) Warning(format string, a ...interface{}) error {
	return nil
}
func (s *LinuxService) Info(format string, a ...interface{}) error {
	return nil
}

const systemVScript = `#!/bin/sh
# For RedHat and cousins:
# chkconfig: - 99 01
# description: {{.Description}}
# processname: {{.Path}}

### BEGIN INIT INFO
# Provides:          {{.Path}}
# Required-Start:
# Required-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: {{.Display}}
# Description:       {{.Description}}
### END INIT INFO

cmd="{{.Path}}"

name=$(basename $0)
pid_file="/var/run/$name.pid"
stdout_log="/var/log/$name.log"
stderr_log="/var/log/$name.err"

get_pid() {
    cat "$pid_file"
}

is_running() {
    [ -f "$pid_file" ] && ps $(get_pid) > /dev/null 2>&1
}

case "$1" in
    start)
        if is_running; then
            echo "Already started"
        else
            echo "Starting $name"
            $cmd >> "$stdout_log" 2>> "$stderr_log" &
            echo $! > "$pid_file"
            if ! is_running; then
                echo "Unable to start, see $stdout_log and $stderr_log"
                exit 1
            fi
        fi
    ;;
    stop)
        if is_running; then
            echo -n "Stopping $name.."
            kill $(get_pid)
            for i in {1..10}
            do
                if ! is_running; then
                    break
                fi
                echo -n "."
                sleep 1
            done
            echo
            if is_running; then
                echo "Not stopped; may still be shutting down or shutdown may have failed"
                exit 1
            else
                echo "Stopped"
                if [ -f "$pid_file" ]; then
                    rm "$pid_file"
                fi
            fi
        else
            echo "Not running"
        fi
    ;;
    restart)
        $0 stop
        if is_running; then
            echo "Unable to stop, will not attempt to start"
            exit 1
        fi
        $0 start
    ;;
    status)
        if is_running; then
            echo "Running"
        else
            echo "Stopped"
            exit 1
        fi
    ;;
    *)
    echo "Usage: $0 {start|stop|restart|status}"
    exit 1
    ;;
esac
exit 0`

const upstartScript = `# {{.Description}}

description     "{{.Display}}"

start on filesystem or runlevel [2345]
stop on runlevel [!2345]

#setuid username

respawn
respawn limit 10 5
umask 022

console none

pre-start script
    test -x {{.Path}} || { stop; exit 0; }
end script

# Start
exec {{.Path}}
`

const systemdScript = `[Unit]
Description={{.Description}}
ConditionFileIsExecutable={{.Path}}

[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{.Path}}

[Install]
WantedBy=multi-user.target
`