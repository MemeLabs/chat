package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"time"

	parser "github.com/MemeLabs/chat-parser"
	"mvdan.cc/xurls/v2"
)

var entities *EntityExtractor

func init() {
	var err error
	entities, err = NewEntityExtractor()
	if err != nil {
		log.Fatal(err)
	}

	go entities.scheduleEmoteSync()
}

func loadEmoteManifest() ([]string, error) {
	resp, err := http.Get("https://chat.strims.gg/emote-manifest.json")
	if err != nil {
		return nil, fmt.Errorf("failed to get emotes: %w", err)
	}
	defer resp.Body.Close()
	manifest := struct {
		Emotes []struct {
			Name string `json:"name"`
		} `json:"emotes"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to parse emotes manifest: %w", err)
	}

	emotes := make([]string, len(manifest.Emotes))
	for i, e := range manifest.Emotes {
		emotes[i] = e.Name
	}
	return emotes, nil
}

func NewEntityExtractor() (*EntityExtractor, error) {
	emotes, err := loadEmoteManifest()
	if err != nil {
		return nil, err
	}

	return &EntityExtractor{
		parserCtx: parser.NewParserContext(parser.ParserContextValues{
			Emotes:         emotes,
			Nicks:          []string{},
			Tags:           []string{"nsfw", "weeb", "nsfl", "loud"},
			EmoteModifiers: []string{"mirror", "flip", "rain", "snow", "rustle", "worth", "love", "spin", "wide", "lag", "hyper"},
		}),
		urls: xurls.Relaxed(),
	}, nil
}

type EntityExtractor struct {
	parserCtx *parser.ParserContext
	urls      *regexp.Regexp
}

func (x *EntityExtractor) scheduleEmoteSync() {
	for range time.NewTicker(time.Minute).C {
		emotes, err := loadEmoteManifest()
		if err != nil {
			log.Println("failed to update emotes: %v", err)
			continue
		}
		x.parserCtx.Emotes.Replace(parser.RunesFromStrings(emotes))
	}
}

func (x *EntityExtractor) AddNick(emote string) {
	x.parserCtx.Nicks.Insert([]rune(emote))
}

func (x *EntityExtractor) RemoveNick(emote string) {
	x.parserCtx.Nicks.Remove([]rune(emote))
}

func (x *EntityExtractor) Extract(msg string) *Entities {
	e := &Entities{}
	addEntitiesFromSpan(e, parser.NewParser(x.parserCtx, parser.NewLexer(msg)).ParseMessage())

	for _, b := range x.urls.FindAllStringIndex(msg, -1) {
		e.Links = append(e.Links, &Link{
			URL:    msg[b[0]:b[1]],
			Bounds: [2]int{b[0], b[1]},
		})
	}

	return e
}

func addEntitiesFromSpan(e *Entities, span *parser.Span) {
	switch span.Type {
	case parser.SpanCode:
		e.Codes = append(e.Codes, &Code{
			Bounds: [2]int{span.Pos(), span.End()},
		})
	case parser.SpanSpoiler:
		e.Spoilers = append(e.Spoilers, &Spoiler{
			Bounds: [2]int{span.Pos(), span.End()},
		})
	case parser.SpanGreentext:
		e.Greentext = &Generic{
			Bounds: [2]int{span.Pos(), span.End()},
		}
	case parser.SpanMe:
		e.Me = &Generic{
			Bounds: [2]int{span.Pos(), span.End()},
		}
	}

	for _, ni := range span.Nodes {
		switch n := ni.(type) {
		case *parser.Emote:
			e.Emotes = append(e.Emotes, &Emote{
				Name:      n.Name,
				Modifiers: n.Modifiers,
				Bounds:    [2]int{n.Pos(), n.End()},
			})
		case *parser.Nick:
			e.Nicks = append(e.Nicks, &Nick{
				Nick:   n.Nick,
				Bounds: [2]int{n.Pos(), n.End()},
			})
		case *parser.Tag:
			e.Tags = append(e.Tags, &Tag{
				Name:   n.Name,
				Bounds: [2]int{n.Pos(), n.End()},
			})
		case *parser.Span:
			addEntitiesFromSpan(e, n)
		}
	}
}

type Link struct {
	URL    string `json:"url,omitempty"`
	Bounds [2]int `json:"bounds,omitempty"`
}

type Emote struct {
	Name      string   `json:"name,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
	Bounds    [2]int   `json:"bounds,omitempty"`
}

type Nick struct {
	Nick   string `json:"nick,omitempty"`
	Bounds [2]int `json:"bounds,omitempty"`
}

type Tag struct {
	Name   string `json:"name,omitempty"`
	Bounds [2]int `json:"bounds,omitempty"`
}

type Code struct {
	Bounds [2]int `json:"bounds,omitempty"`
}

type Spoiler struct {
	Bounds [2]int `json:"bounds,omitempty"`
}

type Generic struct {
	Bounds [2]int `json:"bounds,omitempty"`
}

type Entities struct {
	Links     []*Link    `json:"links,omitempty"`
	Emotes    []*Emote   `json:"emotes,omitempty"`
	Nicks     []*Nick    `json:"nicks,omitempty"`
	Tags      []*Tag     `json:"tags,omitempty"`
	Codes     []*Code    `json:"codes,omitempty"`
	Spoilers  []*Spoiler `json:"spoilers,omitempty"`
	Greentext *Generic   `json:"greentext,omitempty"`
	Me        *Generic   `json:"me,omitempty"`
}

func getEmotes() ([]string, error) {
	resp, err := http.Get("https://chat.strims.gg/emote-manifest.json")
	if err != nil {
		return nil, fmt.Errorf("failed to request emotes (%d): %v", resp.StatusCode, err)
	}
	defer resp.Body.Close()
	response := struct {
		Emotes []struct {
			Name string `json:"name"`
		} `json:"emotes"`
	}{}
	contents, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read emotes response: %v", err)
	}

	if err = json.Unmarshal(contents, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal emotes response: %v; %v", response, err)
	}

	var emotes []string
	for _, emote := range response.Emotes {
		emotes = append(emotes, emote.Name)
	}
	return emotes, nil
}
