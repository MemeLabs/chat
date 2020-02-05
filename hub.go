package main

import (
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
}

func (hub *Hub) run() {
	pinger := time.NewTicker(PINGINTERVAL)

	for {
		select {
		case c := <-hub.register:
			hub.connections[c] = true
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
			//TODO save into state in case of restart??
			if message.event != "JOIN" && message.event != "QUIT" {
				cacheChatEvent(message)
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

// TODO redis replacement
func cacheChatEvent(msg *message) {
	if len(MSGCACHE) <= 0 {
		return
	}

	MSGLOCK.Lock()
	defer MSGLOCK.Unlock()

	if len(MSGCACHE) >= MSGCACHESIZE {
		MSGCACHE = MSGCACHE[1:]
	}

	data, err := Pack(msg.event, msg.data.([]byte))
	if err != nil {
		D("cacheChatEvent pack error", err)
		return
	}

	MSGCACHE = append(MSGCACHE, string(data[:]))
}

// TODO
func getCache() []string {
	MSGLOCK.RLock()
	defer MSGLOCK.RUnlock()

	out := []string{}
	for _, v := range MSGCACHE {
		if v != "" {
			out = append(out, v)
		}
	}

	return out
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
