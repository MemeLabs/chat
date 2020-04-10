package main

import (
	"reflect"
	"testing"

	parser "github.com/MemeLabs/chat-parser"
	"github.com/davecgh/go-spew/spew"
	"mvdan.cc/xurls/v2"
)

func TestExtractEntities(t *testing.T) {
	tests := []struct {
		input    string
		expected *Entities
	}{
		{"PepeMods", &Entities{Emotes: []*Emote{&Emote{"PepeMods", [2]int{0, 8}}}}},
		{
			"Cinder yes but in retrospect PepeLaugh",
			&Entities{
				Emotes: []*Emote{&Emote{"PepeLaugh", [2]int{29, 38}}},
				Nicks:  []*Nick{&Nick{"Cinder", [2]int{0, 6}}},
			},
		},
	}

	parserCtx := parser.NewParserContext(parser.ParserContextValues{
		Emotes:         []string{"PepeMods", "MiyanoHype", "PepeLaugh"},
		Nicks:          []string{"jbpratt", "slugalisk", "test", "Cinder"},
		Tags:           []string{"nsfw", "weeb", "nsfl", "loud"},
		EmoteModifiers: []string{"mirror", "flip", "rain", "snow", "rustle", "worth", "love", "spin", "wide", "lag", "hyper"},
	})
	rxRelaxed := xurls.Relaxed()
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractEntities(parserCtx, rxRelaxed, tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				spew.Dump(got)
				t.Errorf("got=%v; want=%v", got, tt.expected)
			}
		})
	}
}
