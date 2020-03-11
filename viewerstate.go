package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// NewViewerStateStore create new ViewerStateStore
func NewViewerStateStore() *ViewerStateStore {
	return &ViewerStateStore{
		viewerStatesLock: &sync.RWMutex{},
		viewerStates:     make(map[string]*ViewerState),
		notifyChansLock:  &sync.Mutex{},
		notifyChans:      []chan *ViewerStateChange{},
	}
}

var viewerStates = NewViewerStateStore()

// ViewerStateStore viewer states synced with rustla api
type ViewerStateStore struct {
	viewerStatesLock *sync.RWMutex
	viewerStates     map[string]*ViewerState
	notifyChansLock  *sync.Mutex
	notifyChans      []chan *ViewerStateChange
}

// ViewerStateChange state change event emitted when viewer changes channels,
// comes online or goes offline
type ViewerStateChange struct {
	Nick    string         `json:"nick"`
	Online  bool           `json:"online"`
	Channel *StreamChannel `json:"channel,omitempty"`
}

// ViewerState rustla api viewer state record
type ViewerState struct {
	UserID            string         `json:"user_id"`
	Name              string         `json:"name"`
	Online            bool           `json:"online"`
	EnablePublicState bool           `json:"enable_public_state"`
	StreamID          uint64         `json:"stream_id"`
	Channel           *StreamChannel `json:"channel"`
}

// Equals check if two ViewerStates are identical
func (s *ViewerState) Equals(o *ViewerState) bool {
	if s == nil || o == nil {
		return s == o
	}
	return s.Online == o.Online && s.EnablePublicState == o.EnablePublicState && s.Channel.Equals(o.Channel)
}

// StreamChannel rustla channel definition
type StreamChannel struct {
	Channel string `json:"channel"`
	Service string `json:"service"`
	Path    string `json:"path"`
}

// Equals check if two channels are identical
func (s *StreamChannel) Equals(o *StreamChannel) bool {
	if s == nil || o == nil {
		return s == o
	}
	return s.Channel == o.Channel && s.Service == o.Service && s.Path == o.Path
}

func (v *ViewerStateStore) run() {
	// run rustla api reader with retries on failure
	go func() {
		for {
			if err := v.sync(); err != nil {
				log.Printf("error syncing viewer state: %s", err)
			}
			time.Sleep(time.Second * 30)
		}
	}()

	// broadcast state change events to chat
	go func() {
		changes := make(chan *ViewerStateChange, 4)
		v.NotifyChange(changes)

		for c := range changes {
			data, err := json.Marshal(c)
			if err != nil {
				continue
			}

			hub.broadcast <- &message{
				event: "VIEWERSTATE",
				data:  data,
			}
		}
	}()
}

func (v *ViewerStateStore) sync() error {
	req, err := http.NewRequest("GET", VIEWERSTATEAPI, nil)
	if err != nil {
		return fmt.Errorf("creating http request: %w", err)
	}

	jwt, err := createAPIJWT(APIUSERID)
	if err != nil {
		return fmt.Errorf("creating api jwt: %w", err)
	}
	req.AddCookie(&http.Cookie{
		Name:  JWTCOOKIENAME,
		Value: jwt,
	})

	client := &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   0,
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executing http request: %w", err)
	}

	r := bufio.NewReader(res.Body)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return err
		}
		state := &ViewerState{}
		if err := json.Unmarshal(line, state); err != nil {
			return fmt.Errorf("parsing viewer state: %w", err)
		}
		v.updatePublicState(state)
	}
}

func (v *ViewerStateStore) updatePublicState(state *ViewerState) {
	v.viewerStatesLock.Lock()
	defer v.viewerStatesLock.Unlock()

	prev, ok := v.viewerStates[state.UserID]
	if !state.EnablePublicState || !state.Online {
		if ok {
			delete(v.viewerStates, state.UserID)
			v.emitChange(&ViewerStateChange{
				Nick:   state.Name,
				Online: false,
			})
		}
		return
	}

	if ok && prev.Equals(state) {
		return
	}

	v.viewerStates[state.UserID] = state
	v.emitChange(&ViewerStateChange{
		Nick:    state.Name,
		Online:  true,
		Channel: state.Channel,
	})
}

func (v *ViewerStateStore) emitChange(c *ViewerStateChange) {
	v.notifyChansLock.Lock()
	defer v.notifyChansLock.Unlock()
	for _, ch := range v.notifyChans {
		ch <- c
	}
}

// NotifyChange register channel to be notified when viewer state changes
func (v *ViewerStateStore) NotifyChange(ch chan *ViewerStateChange) {
	v.notifyChansLock.Lock()
	defer v.notifyChansLock.Unlock()
	v.notifyChans = append(v.notifyChans, ch)
}

// DumpChanges dump store to a slice for client sync via http api
func (v *ViewerStateStore) DumpChanges() []ViewerStateChange {
	v.viewerStatesLock.RLock()
	defer v.viewerStatesLock.RUnlock()

	changes := make([]ViewerStateChange, 0, len(v.viewerStates))
	for _, state := range v.viewerStates {
		changes = append(changes, ViewerStateChange{
			Nick:    state.Name,
			Online:  true,
			Channel: state.Channel,
		})
	}

	return changes
}
