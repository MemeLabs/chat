package main

import (
	"math/rand"
)

func TransformRares(msg *EventDataOut) {
	if rand.Float64() > RARECHANCE {
		return
	}

	for _, e := range msg.Entities.Emotes {
		e.Modifiers = append(e.Modifiers, "rare")
	}
}
