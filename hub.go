package main

import (
	"sort"
	"strconv"
	"time"
)

type Hub struct {
	connections map[*Connection]bool
	broadcast   chan *message
	privmsg     chan *PrivmsgOut
	register    chan *Connection
	unregister  chan *Connection
	bans        chan Userid
	ipbans      chan string
	getips      chan useridips
	users       map[Userid]*User
	refreshuser chan Userid
	// lastEventID is the id given to the most recent broadcast event. Only
	// hub.run touches it.
	lastEventID int64
}

type useridips struct {
	userid Userid
	c      chan []string
}

var hub = Hub{
	connections: make(map[*Connection]bool),
	broadcast:   make(chan *message, BROADCASTCHANNELSIZE),
	privmsg:     make(chan *PrivmsgOut, BROADCASTCHANNELSIZE),
	register:    make(chan *Connection, 256),
	unregister:  make(chan *Connection),
	bans:        make(chan Userid, 4),
	ipbans:      make(chan string, 4),
	getips:      make(chan useridips),
	users:       make(map[Userid]*User),
	refreshuser: make(chan Userid, 4),
	// seed event ids from the clock so they keep increasing across restarts
	// and clients resuming from a previous process still get new events.
	lastEventID: unixMilliTime() * 1000,
}

type cachedEvent struct {
	id     int64
	packed string
}

type replayOut struct {
	Complete bool     `json:"complete"`
	Events   []string `json:"events"`
}

func (hub *Hub) run() {
	pinger := time.NewTicker(PINGINTERVAL)

	for {
		select {
		case c := <-hub.register:
			hub.connections[c] = true
			// the snapshot and registration both happen on this goroutine, so
			// the replay ends exactly where live delivery to c begins.
			if c.since > 0 {
				hub.replay(c)
			}
		case c := <-hub.unregister:
			delete(hub.connections, c)
		case userid := <-hub.refreshuser:
			for c, _ := range hub.connections {
				if c.user != nil && c.user.id == userid {
					go c.Refresh()
				}
			}
		case userid := <-hub.bans:
			for c, _ := range hub.connections {
				if c.user != nil && c.user.id == userid {
					go c.Banned()
				}
			}
		case stringip := <-hub.ipbans:
			for c := range hub.connections {
				if c.ip == stringip {
					DP("Found connection to ban with ip", stringip, "user", c.user)
					go c.Banned()
				}
			}
		case d := <-hub.getips:
			ips := make([]string, 0, 3)
			for c, _ := range hub.connections {
				if c.user != nil && c.user.id == d.userid {
					ips = append(ips, c.ip)
				}
			}
			d.c <- ips
		case message := <-hub.broadcast:
			// TODO should be channel, could lock up...
			// TODO save into state in case of restart??
			hub.lastEventID++
			id := hub.lastEventID
			message.data = withEventID(message.data.([]byte), id)

			if message.event != "JOIN" && message.event != "QUIT" && message.event != "VIEWERSTATE" {
				cacheChatEvent(message, id)
			}

			for c := range hub.connections {
				if len(c.sendmarshalled) < SENDCHANNELSIZE {
					c.sendmarshalled <- message
				}
			}
		case p := <-hub.privmsg:
			for c, _ := range hub.connections {
				if c.user != nil && c.user.id == p.targetuid {
					if len(c.sendmarshalled) < SENDCHANNELSIZE {
						c.sendmarshalled <- &p.message
					}
				}
			}
		// timeout handling
		case t := <-pinger.C:
			for c := range hub.connections {
				if c.ping != nil && len(c.ping) < 2 {
					c.ping <- t
				} else if c.ping != nil {
					close(c.ping)
					c.ping = nil
				}
			}
		}
	}
}

// withEventID inserts an "id" field at the start of a marshalled JSON object.
// Payloads that are not JSON objects are returned unchanged.
func withEventID(data []byte, id int64) []byte {
	if len(data) < 2 || data[0] != '{' {
		return data
	}

	out := make([]byte, 0, len(data)+24)
	out = append(out, `{"id":`...)
	out = strconv.AppendInt(out, id, 10)
	if data[1] != '}' {
		out = append(out, ',')
	}
	return append(out, data[1:]...)
}

// replay sends c the cached events it missed since c.since.
func (hub *Hub) replay(c *Connection) {
	events, complete := getCacheSince(c.since)
	data, err := Marshal(&replayOut{Complete: complete, Events: events})
	if err != nil {
		D("replay marshal error", err)
		return
	}

	select {
	case c.sendmarshalled <- &message{event: "REPLAY", data: data}:
	default:
	}
}

// TODO redis replacement
func cacheChatEvent(msg *message, id int64) {
	if MSGCACHESIZE <= 0 {
		return
	}

	data, err := Pack(msg.event, msg.data.([]byte))
	if err != nil {
		D("cacheChatEvent pack error", err)
		return
	}

	MSGLOCK.Lock()
	defer MSGLOCK.Unlock()

	if len(MSGCACHE) >= MSGCACHESIZE {
		MSGEVICTEDID = MSGCACHE[0].id
		MSGCACHE = MSGCACHE[1:]
	}

	MSGCACHE = append(MSGCACHE, cachedEvent{id, string(data)})
}

func getCache() []string {
	MSGLOCK.RLock()
	defer MSGLOCK.RUnlock()

	out := make([]string, 0, len(MSGCACHE))
	for _, v := range MSGCACHE {
		out = append(out, v.packed)
	}

	return out
}

// getCacheSince returns the cached events with ids greater than since, and
// whether that covers every event since then (none were evicted).
func getCacheSince(since int64) ([]string, bool) {
	MSGLOCK.RLock()
	defer MSGLOCK.RUnlock()

	i := sort.Search(len(MSGCACHE), func(i int) bool { return MSGCACHE[i].id > since })
	out := make([]string, 0, len(MSGCACHE)-i)
	for _, v := range MSGCACHE[i:] {
		out = append(out, v.packed)
	}

	return out, MSGEVICTEDID <= since
}

func (hub *Hub) getIPsForUserid(userid Userid) []string {
	c := make(chan []string, 1)
	hub.getips <- useridips{userid, c}
	return <-c
}

func (hub *Hub) canUserSpeak(c *Connection) bool {
	state.RLock()
	defer state.RUnlock()

	if !state.submode || c.user.isSubscriber() {
		return true
	}

	return false
}

func (hub *Hub) toggleSubmode(enabled bool) {
	state.Lock()
	defer state.Unlock()

	state.submode = enabled
	state.save()
}
