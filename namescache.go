package main

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

type namesCache struct {
	users           map[Userid]*User
	marshallednames []byte
	connectioncount uint32
	ircnames        [][]string
	sync.RWMutex
}

type namesOut struct {
	Users       []*SimplifiedUser `json:"users"`
	Connections uint32            `json:"connectioncount"`
}

var namescache = namesCache{
	users:   make(map[Userid]*User),
	RWMutex: sync.RWMutex{},
}

func (nc *namesCache) updateNames() {

	users := make([]*SimplifiedUser, 0, len(nc.users))

	for _, u := range nc.users {
		u.RLock()
		n := atomic.LoadInt32(&u.connections)
		u.RUnlock()
		if n <= 0 {
			// should not happen anymore since we remove users with 0 connections now.
			continue
		}
		users = append(users, u.simplified)
	}

	n := namesOut{
		Users:       users,
		Connections: nc.connectioncount,
	}

	var err error
	nc.marshallednames, err = json.Marshal(n)
	if err != nil {
		B(err)
	}
}

func (nc *namesCache) getNames() []byte {
	nc.RLock()
	defer nc.RUnlock()
	return nc.marshallednames
}

func (nc *namesCache) get(id Userid) *User {
	nc.RLock()
	defer nc.RUnlock()
	u := nc.users[id]
	return u
}

func (nc *namesCache) add(user *User) *User {
	nc.Lock()
	defer nc.Unlock()

	nc.connectioncount++
	if u, ok := nc.users[user.id]; ok {
		atomic.AddInt32(&u.connections, 1)
	} else {
		atomic.AddInt32(&user.connections, 1)
		su := &SimplifiedUser{
			Nick:     user.nick,
			Features: user.simplified.Features,
		}
		user.simplified = su
		nc.users[user.id] = user
	}

	nc.updateNames()
	return nc.users[user.id]
}

func (nc *namesCache) disconnect(user *User) {
	nc.Lock()
	defer nc.Unlock()

	if user != nil {
		nc.connectioncount--
		if u, ok := nc.users[user.id]; ok {
			conncount := atomic.AddInt32(&u.connections, -1)
			if conncount <= 0 {
				delete(nc.users, user.id)
			}
		}

	} else {
		nc.connectioncount--
	}
	nc.updateNames()
}

func (nc *namesCache) refresh(user *User) {
	nc.RLock()
	defer nc.RUnlock()

	if u, ok := nc.users[user.id]; ok {
		u.Lock()
		u.simplified.Nick = user.nick
		u.simplified.Features = user.simplified.Features
		u.nick = user.nick
		u.features = user.features
		u.Unlock()
		nc.updateNames()
	}
}

func (nc *namesCache) addConnection() {
	nc.Lock()
	defer nc.Unlock()
	nc.connectioncount++
	nc.updateNames()
}
