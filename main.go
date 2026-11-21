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
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"gopkg.in/ini.v1"
)

type State struct {
	mutes   map[Userid]time.Time
	submode bool
	sync.RWMutex
}

var state = &State{mutes: make(map[Userid]time.Time)}

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
	MSGCACHE         = []cachedEvent{} // TODO redis replacement...
	MSGCACHESIZE     = 150
	MSGEVICTEDID     int64 // id of the newest event dropped from MSGCACHE
	MSGLOCK          sync.RWMutex
	RARECHANCE       = 0.00001
	EMOTEMANIFEST    = "http://localhost:18078/emote-manifest.json"
)

func main() {
	c, err := ini.LoadSources(iniOptions, "settings.cfg")
	if err != nil {
		nc := ini.Empty(iniOptions)
		d := nc.Section("default")
		d.Comment = "# ChatBackend"
		d.NewKey("debug", "false")
		d.NewKey("listenaddress", ":9998")
		d.NewKey("maxprocesses", "0")
		d.NewKey("chatdelay", fmt.Sprintf("%d", 300*time.Millisecond))
		d.NewKey("maxthrottletime", fmt.Sprintf("%d", 5*time.Minute))
		d.NewKey("dbfile", "chatbackend.sqlite")
		d.NewKey("jwtcookiename", JWTCOOKIENAME)
		d.NewKey("jwtsecret", "")
		d.NewKey("apiuserid", "")
		d.NewKey("usernameapi", USERNAMEAPI)
		d.NewKey("viewerstateapi", VIEWERSTATEAPI)
		d.NewKey("messagecachesize", "150")
		d.NewKey("rarechance", strconv.FormatFloat(RARECHANCE, 'f', -1, 64))
		d.NewKey("emotemanifest", EMOTEMANIFEST)
		d.NewKey("initdb", "false")

		if err = writeConfigFile(nc, "settings.cfg"); err != nil {
			log.Fatal("Unable to create settings.cfg: ", err)
		}
		if c, err = ini.LoadSources(iniOptions, "settings.cfg"); err != nil {
			log.Fatal("Unable to read settings.cfg: ", err)
		}
	}
	cfg := c.Section("default")

	debuggingenabled, _ = cfg.Key("debug").Bool()
	addr := cfg.Key("listenaddress").String()
	processes, _ := cfg.Key("maxprocesses").Int64()
	delay, _ := cfg.Key("chatdelay").Int64()
	maxthrottletime, _ := cfg.Key("maxthrottletime").Int64()
	dbfile := cfg.Key("dbfile").String()
	initdb, _ := cfg.Key("initdb").Bool()
	DELAY = time.Duration(delay)
	MAXTHROTTLETIME = time.Duration(maxthrottletime)
	JWTSECRET = cfg.Key("jwtsecret").String()
	JWTCOOKIENAME = cfg.Key("jwtcookiename").String()
	APIUSERID = cfg.Key("apiuserid").String()
	USERNAMEAPI = cfg.Key("usernameapi").String()
	VIEWERSTATEAPI = cfg.Key("viewerstateapi").String()
	msgcachesize, _ := cfg.Key("messagecachesize").Int64()
	RARECHANCE, _ = cfg.Key("rarechance").Float64()
	EMOTEMANIFEST = cfg.Key("emotemanifest").String()

	if JWTSECRET == "" {
		JWTSECRET = "PepoThink"
		fmt.Println("Insecurely using default JWT secret")
	}
	if msgcachesize >= 0 {
		MSGCACHESIZE = int(msgcachesize)
	}
	MSGCACHE = make([]cachedEvent, 0, MSGCACHESIZE)

	if processes <= 0 {
		processes = int64(runtime.NumCPU())
	}
	runtime.GOMAXPROCS(int(processes))

	state.load()
	initDatabase(dbfile, initdb)
	initEntities()

	go hub.run()
	go bans.run()
	go viewerStates.run()

	var checkOrigin func(r *http.Request) bool
	if debuggingenabled {
		checkOrigin = func(r *http.Request) bool { return true }
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     checkOrigin,
	}

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

		// id of the last event the client received, to replay what it missed
		since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil || since < 0 {
			since = 0
		}

		newConnection(ws, user, ip, since)
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

	b, err := os.ReadFile(".state.dc")
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

	err = os.WriteFile(".state.dc", mb.Bytes(), 0600)
	if err != nil {
		D("Error with writing out state file:", err)
	}
}

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

func createAPIJWT(userID string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":  userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return token.SignedString([]byte(JWTSECRET))
}
