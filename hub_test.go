package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestWithEventID(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{`{"nick":"a","timestamp":1}`, `{"id":42,"nick":"a","timestamp":1}`},
		{`{}`, `{"id":42}`},
		{`"text"`, `"text"`},
		{`[1,2]`, `[1,2]`},
		{``, ``},
	}

	for _, c := range cases {
		if got := string(withEventID([]byte(c.in), 42)); got != c.out {
			t.Errorf("withEventID(%q) = %q, want %q", c.in, got, c.out)
		}
	}

	out := &EventDataOut{SimplifiedUser: &SimplifiedUser{Nick: "a"}, Timestamp: 1, Data: "hi"}
	data, _ := Marshal(out)
	var decoded map[string]interface{}
	if err := json.Unmarshal(withEventID(data, 7), &decoded); err != nil {
		t.Fatalf("spliced payload is not valid json: %v", err)
	}
	if decoded["id"] != float64(7) || decoded["nick"] != "a" || decoded["data"] != "hi" {
		t.Errorf("unexpected decoded payload %v", decoded)
	}
}

func resetCache(size int) {
	MSGCACHESIZE = size
	MSGCACHE = make([]cachedEvent, 0, size)
	MSGEVICTEDID = 0
}

func cacheTestEvents(ids ...int64) {
	for _, id := range ids {
		data := withEventID([]byte(fmt.Sprintf(`{"data":"%d"}`, id)), id)
		cacheChatEvent(&message{event: "MSG", data: data}, id)
	}
}

func packedTestEvents(ids ...int64) []string {
	out := []string{}
	for _, id := range ids {
		out = append(out, fmt.Sprintf(`MSG {"id":%d,"data":"%d"}`, id, id))
	}
	return out
}

func TestGetCacheSince(t *testing.T) {
	defer resetCache(150)

	cases := []struct {
		name     string
		size     int
		cached   []int64
		since    int64
		events   []string
		complete bool
	}{
		{"empty cache", 5, nil, 10, packedTestEvents(), true},
		{"caught up", 5, []int64{10, 12, 13}, 13, packedTestEvents(), true},
		{"since inside cache", 5, []int64{10, 12, 13}, 11, packedTestEvents(12, 13), true},
		{"since between ids", 5, []int64{10, 12, 13}, 12, packedTestEvents(13), true},
		{"since before oldest, nothing evicted", 5, []int64{10, 12}, 3, packedTestEvents(10, 12), true},
		{"missed events evicted", 2, []int64{10, 11, 12, 13}, 10, packedTestEvents(12, 13), false},
		{"evicted events already seen", 2, []int64{10, 11, 12, 13}, 11, packedTestEvents(12, 13), true},
		{"since from an earlier process", 5, []int64{1000, 1001}, 20, packedTestEvents(1000, 1001), true},
	}

	for _, c := range cases {
		resetCache(c.size)
		cacheTestEvents(c.cached...)
		events, complete := getCacheSince(c.since)
		if !reflect.DeepEqual(events, c.events) || complete != c.complete {
			t.Errorf("%s: getCacheSince(%d) = %v, %v; want %v, %v", c.name, c.since, events, complete, c.events, c.complete)
		}
	}
}

func TestGetCacheKeepsHistoryFormat(t *testing.T) {
	defer resetCache(150)

	resetCache(2)
	cacheTestEvents(1, 2, 3)
	if got, want := getCache(), packedTestEvents(2, 3); !reflect.DeepEqual(got, want) {
		t.Errorf("getCache() = %v, want %v", got, want)
	}

	resetCache(0)
	cacheTestEvents(1)
	if got := getCache(); len(got) != 0 {
		t.Errorf("cache with size 0 should stay empty, got %v", got)
	}
}
