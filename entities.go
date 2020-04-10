package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"

	parser "github.com/MemeLabs/chat-parser"
)

func extractEntities(parserCtx *parser.ParserContext, urls *regexp.Regexp, msg string) *Entities {
	e := &Entities{}
	addEntitiesFromSpan(e, parser.NewParser(parserCtx, parser.NewLexer(msg)).ParseMessage())

	for _, b := range urls.FindAllStringIndex(msg, -1) {
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
		e.Greentexts = append(e.Greentexts, &Greentext{
			Bounds: [2]int{span.Pos(), span.End()},
		})
	}

	for _, ni := range span.Nodes {
		switch n := ni.(type) {
		case *parser.Emote:
			e.Emotes = append(e.Emotes, &Emote{
				Name:   n.Name,
				Bounds: [2]int{n.Pos(), n.End()},
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
	Name   string `json:"name,omitempty"`
	Bounds [2]int `json:"bounds,omitempty"`
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

type Greentext struct {
	Bounds [2]int `json:"bounds,omitempty"`
}

type Entities struct {
	Links      []*Link      `json:"links,omitempty"`
	Emotes     []*Emote     `json:"emotes,omitempty"`
	Nicks      []*Nick      `json:"nicks,omitempty"`
	Tags       []*Tag       `json:"tags,omitempty"`
	Codes      []*Code      `json:"codes,omitempty"`
	Spoilers   []*Spoiler   `json:"spoilers,omitempty"`
	Greentexts []*Greentext `json:"greentexts,omitempty"`
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
