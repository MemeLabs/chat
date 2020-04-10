/*
Based on https://github.com/trevex/golem
Licensed under the Apache License, Version 2.0
http://www.apache.org/licenses/LICENSE-2.0.html
*/
package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	_ "expvar"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/gorilla/websocket"
	conf "github.com/msbranco/goconfig"
)

type State struct {
	mutes   map[Userid]time.Time
	submode bool
	sync.RWMutex
}

var state = &State{mutes: make(map[Userid]time.Time)}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

const (
	WRITETIMEOUT         = 10 * time.Second
	READTIMEOUT          = time.Minute
	PINGINTERVAL         = 10 * time.Second
	PINGTIMEOUT          = 30 * time.Second
	MAXMESSAGESIZE       = 6144 // 512 max chars in a message, 8bytes per chars possible, plus factor in some protocol overhead
	SENDCHANNELSIZE      = 16
	BROADCASTCHANNELSIZE = 256
	DEFAULTBANDURATION   = time.Hour
	DEFAULTMUTEDURATION  = 10 * time.Minute
)

var (
	debuggingenabled = false
	DELAY            = 300 * time.Millisecond
	MAXTHROTTLETIME  = 5 * time.Minute
	JWTSECRET        = ""
	JWTCOOKIENAME    = "jwt"
	APIUSERID        = ""
	USERNAMEAPI      = "http://localhost:8076/api/username/"
	VIEWERSTATEAPI   = "http://localhost:8076/api/admin/viewer-state"
	MSGCACHE         = []string{} // TODO redis replacement...
	MSGCACHESIZE     = 150
	MSGLOCK          sync.RWMutex
)

func main() {
	c, err := conf.ReadConfigFile("settings.cfg")
	if err != nil {
		nc := conf.NewConfigFile()
		nc.AddOption("default", "debug", "false")
		nc.AddOption("default", "listenaddress", ":9998")
		nc.AddOption("default", "maxprocesses", "0")
		nc.AddOption("default", "chatdelay", fmt.Sprintf("%d", 300*time.Millisecond))
		nc.AddOption("default", "maxthrottletime", fmt.Sprintf("%d", 5*time.Minute))
		nc.AddOption("default", "dbfile", "chatbackend.sqlite")
		nc.AddOption("default", "jwtcookiename", JWTCOOKIENAME)
		nc.AddOption("default", "jwtsecret", "")
		nc.AddOption("default", "apiuserid", "")
		nc.AddOption("default", "usernameapi", USERNAMEAPI)
		nc.AddOption("default", "viewerstateapi", VIEWERSTATEAPI)
		nc.AddOption("default", "messagecachesize", "150")

		if err := nc.WriteConfigFile("settings.cfg", 0644, "ChatBackend"); err != nil {
			log.Fatal("Unable to create settings.cfg: ", err)
		}
		if c, err = conf.ReadConfigFile("settings.cfg"); err != nil {
			log.Fatal("Unable to read settings.cfg: ", err)
		}
	}

	debuggingenabled, _ = c.GetBool("default", "debug")
	addr, _ := c.GetString("default", "listenaddress")
	processes, _ := c.GetInt64("default", "maxprocesses")
	delay, _ := c.GetInt64("default", "chatdelay")
	maxthrottletime, _ := c.GetInt64("default", "maxthrottletime")
	dbfile, _ := c.GetString("default", "dbfile")
	DELAY = time.Duration(delay)
	MAXTHROTTLETIME = time.Duration(maxthrottletime)
	JWTSECRET, _ = c.GetString("default", "jwtsecret")
	JWTCOOKIENAME, _ = c.GetString("default", "jwtcookiename")
	APIUSERID, _ = c.GetString("default", "apiuserid")
	USERNAMEAPI, _ = c.GetString("default", "usernameapi")
	VIEWERSTATEAPI, _ = c.GetString("default", "viewerstateapi")
	msgcachesize, _ := c.GetInt64("default", "messagecachesize")

	if JWTSECRET == "" {
		JWTSECRET = "PepoThink"
		fmt.Println("Insecurely using default JWT secret")
	}
	if msgcachesize >= 0 {
		MSGCACHESIZE = int(msgcachesize)
	}
	MSGCACHE = make([]string, MSGCACHESIZE)

	if processes <= 0 {
		processes = int64(runtime.NumCPU())
	}
	runtime.GOMAXPROCS(int(processes))

	state.load()
	initDatabase(dbfile)

	go hub.run()
	go bans.run()
	go viewerStates.run()

	// TODO hacked in api for compat
	http.HandleFunc("/api/chat/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		jwtcookie, err := r.Cookie(JWTCOOKIENAME)
		if err != nil {
			http.Error(w, "Not logged in", 401)
			return
		}
		claims, err := parseJwt(jwtcookie.Value)
		if err != nil {
			http.Error(w, "Not logged in", 401)
			return
		}
		username, err := userFromAPI(claims.UserId)
		if err != nil {
			http.Error(w, "Really makes you think", 401)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"username":"%s", "nick":"%s"}`, username, username)))
	})

	// TODO cache foo
	http.HandleFunc("/api/chat/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		history, err := json.Marshal(getCache())
		if err != nil {
			http.Error(w, "", 500)
			return
		}
		w.Write(history)
	})

	// TODO cache foo
	http.HandleFunc("/api/chat/viewer-states", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(viewerStates.DumpChanges()); err != nil {
			http.Error(w, "[]", 500)
		}
	})

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		user, banned, ip := getUserFromWebRequest(r)

		if banned {
			ws.SetWriteDeadline(time.Now().Add(WRITETIMEOUT))
			ws.WriteMessage(websocket.TextMessage, []byte(`ERR "banned"`))
			return
		}

		newConnection(ws, user, ip)
	})

	fmt.Printf("Using %v threads, and listening on: %v\n", processes, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func getMaskedIP(s string) string {
	ipv6mask := net.CIDRMask(64, 128)
	ip := net.ParseIP(s)
	if ip.To4() == nil {
		return ip.Mask(ipv6mask).String()
	}
	return s
}

func unixMilliTime() int64 {
	return time.Now().UTC().Truncate(time.Millisecond).UnixNano() / int64(time.Millisecond)
}

// expecting the argument to be in UTC
func isExpiredUTC(t time.Time) bool {
	return t.Before(time.Now().UTC())
}

func addDurationUTC(d time.Duration) time.Time {
	return time.Now().UTC().Add(d)
}

func getFuturetimeUTC() time.Time {
	return time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func (s *State) load() {
	s.Lock()
	defer s.Unlock()

	b, err := ioutil.ReadFile(".state.dc")
	if err != nil {
		D("Error while reading from states file", err)
		return
	}
	mb := bytes.NewBuffer(b)
	dec := gob.NewDecoder(mb)
	err = dec.Decode(&s.mutes)
	if err != nil {
		D("Error decoding mutes from states file", err)
	}
	err = dec.Decode(&s.submode)
	if err != nil {
		D("Error decoding submode from states file", err)
	}
}

// expects to be called with locks held
func (s *State) save() {
	mb := new(bytes.Buffer)
	enc := gob.NewEncoder(mb)
	err := enc.Encode(&s.mutes)
	if err != nil {
		D("Error encoding mutes:", err)
	}
	err = enc.Encode(&s.submode)
	if err != nil {
		D("Error encoding submode:", err)
	}

	err = ioutil.WriteFile(".state.dc", mb.Bytes(), 0600)
	if err != nil {
		D("Error with writing out state file:", err)
	}
}

func createAPIJWT(userID string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":  userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return token.SignedString([]byte(JWTSECRET))
}
