package main

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/ini.v1"
)

var debuggingenabled bool
var (
	dialer = websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	headers = http.Header{
		"Origin": []string{"http://localhost"},
	}
)

var (
	mu           = sync.Mutex{}
	pingrunning  = false
	namesrunning = false
)

// iniOptions match the old goconfig parsing: option names are
// case-insensitive and values are never truncated at '#' or ';'.
var iniOptions = ini.LoadOptions{Insensitive: true, IgnoreInlineComment: true}

func writeConfigFile(f *ini.File, path string) error {
	// "default" is ini's implicit section; keep the explicit [default] header
	// that existing config files use.
	ini.DefaultHeader = true
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func main() {
	c, err := ini.LoadSources(iniOptions, "angel.cfg")
	if err != nil {
		nc := ini.Empty(iniOptions)
		d := nc.Section("default")
		d.Comment = "# Chat Angel, watching over the chat and restarting it as needed"
		d.NewKey("debug", "false")
		d.NewKey("binarypath", "./")
		d.NewKey("serverurl", "ws://localhost:9998/ws")
		d.NewKey("origin", "http://localhost")

		if err := writeConfigFile(nc, "angel.cfg"); err != nil {
			log.Fatal("Unable to create angel.cfg: ", err)
		}
		if c, err = ini.LoadSources(iniOptions, "angel.cfg"); err != nil {
			log.Fatal("Unable to read angel.cfg: ", err)
		}
	}
	cfg := c.Section("default")

	debuggingenabled, _ = cfg.Key("debug").Bool()
	binpath := cfg.Key("binarypath").String()
	serverurl := cfg.Key("serverurl").String()
	origin := cfg.Key("origin").String()
	initLog()
	headers.Set("Origin", origin)

	base := path.Base(binpath)
	basedir := strings.TrimSuffix(binpath, "/"+base)
	os.Chdir(basedir) // so that the chat can still read the settings.cfg

	shouldrestart := make(chan bool)
	processexited := make(chan bool)
	t := time.NewTicker(10 * time.Second)
	sct := make(chan os.Signal, 1)
	signal.Notify(sct, syscall.SIGTERM)

	go (func() {
		for _ = range sct {
			P("CAUGHT SIGTERM, restarting the chat")
			shouldrestart <- true
		}
	})()

	var restarting bool

again:
	restarting = true
	initLogWriter()
	cmd := exec.Command(binpath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		F("Stdoutpipe error: ", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		F("Stderrpipe error: ", err)
	}

	go accumulateLog(stdout)
	go accumulateLog(stderr)

	if err := cmd.Start(); err != nil {
		P("Error starting", binpath, err)
		return
	}

	go (func() {
		if err := cmd.Wait(); err != nil {
			P("Error while waiting for process to exit ", err)
		}
		P("Chat process exited, restarting")
		processexited <- true
	})()

	time.Sleep(10 * time.Second)
	restarting = false

	for {
		select {
		case <-t.C:
			if !restarting {
				go checkNames(serverurl, shouldrestart)
				go checkPing(serverurl, shouldrestart)
			}
		case <-shouldrestart:
			if !restarting {
				cmd.Process.Signal(syscall.SIGTERM)
			} else {
				P("Received from shouldrestart but already restarting, ignored")
			}
			// TODO move pprof files out of the dir
		case <-processexited:
			if !restarting {
				time.Sleep(200 * time.Millisecond)
				goto again
			}
		}
	}
}

func checkPing(serverurl string, shouldrestart chan bool) {
	mu.Lock()
	if pingrunning {
		mu.Unlock()
		return
	}
	pingrunning = true
	mu.Unlock()
	defer (func() {
		mu.Lock()
		pingrunning = false
		mu.Unlock()
	})()

	var pongreceived bool
	ws, _, err := dialer.Dial(serverurl, headers)
	if err != nil {
		P("Unable to connect to ", serverurl)
		shouldrestart <- true
		return
	}

	defer ws.Close()
	start := time.Now()

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	ws.SetPingHandler(func(m string) error {
		return ws.WriteMessage(websocket.PongMessage, []byte(m))
	})
	ws.SetPongHandler(func(m string) error {
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		pongreceived = true
		return nil
	})
	ws.WriteMessage(websocket.PingMessage, []byte{})

checkpingagain:
	_, _, err = ws.ReadMessage()
	if !pongreceived && err != nil {
		B("Unable to read from the websocket ", err)
		shouldrestart <- true
		return
	}

	if !pongreceived && time.Since(start) > 5*time.Second {
		P("Didnt receive PONG in 5s, restarting")
		shouldrestart <- true
		return
	}

	if !pongreceived {
		goto checkpingagain
	}

	D("PING check OK")
}

func checkNames(serverurl string, shouldrestart chan bool) {
	mu.Lock()
	if namesrunning {
		mu.Unlock()
		return
	}
	namesrunning = true
	mu.Unlock()
	defer (func() {
		mu.Lock()
		namesrunning = false
		mu.Unlock()
	})()

	ws, _, err := dialer.Dial(serverurl, headers)
	if err != nil {
		P("Unable to connect to ", serverurl)
		shouldrestart <- true
		return
	}

	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	ws.SetPingHandler(func(m string) error {
		return ws.WriteMessage(websocket.PongMessage, []byte(m))
	})

	defer ws.Close()
	start := time.Now()

checknamesagain:
	msgtype, message, err := ws.ReadMessage()
	if err != nil {
		B("Unable to read from the websocket ", err)
		shouldrestart <- true
		return
	}

	if time.Since(start) > 5*time.Second {
		P("Didnt receive NAMES in 5s, restarting")
		shouldrestart <- true
		return
	}

	if msgtype != websocket.TextMessage || string(message[:5]) != "NAMES" {
		goto checknamesagain
	}

	D("NAMES check OK")
}
