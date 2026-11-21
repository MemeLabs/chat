package main

import (
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

func init() {
	initDatabase(":memory:", true)
}

/*
// this needs to be fixed along with `nicklookup`

func TestUserLookup(t *testing.T) {
	uid := Userid(1)
	nick := "testnick"

	u := &User{}
	u.id = uid
	u.nick = nick

	usertools.addUser(u, true)
	if r, _ := usertools.getUseridForNick(nick); r != uid {
		t.Error("usertools.adduser failed, returned uid was: ", r, "(expected:", uid, ") for nick: ", nick)
	}

	nick = "TESTNICK"
	if r, _ := usertools.getUseridForNick(nick); r != uid {
		t.Error("usertools.adduser failed, returned uid was: ", r, "(expected:", uid, ") for nick: ", nick)
	}
}
*/

func TestFeatures(t *testing.T) {
	uid := Userid(1)
	nick := "testnick"

	u := &User{}
	u.id = uid
	u.nick = nick

	if u.featureGet(ISPROTECTED) {
		t.Error("feature should not be set")
	}
	if u.featureGet(ISSUBSCRIBER) {
		t.Error("feature should not be set")
	}
	if u.featureGet(ISVIP) {
		t.Error("feature should not be set")
	}
	if u.featureGet(ISMODERATOR) {
		t.Error("feature should not be set")
	}
	if u.featureGet(ISADMIN) {
		t.Error("feature should not be set")
	}
	if u.featureGet(ISBOT) {
		t.Error("feature should not be set")
	}
	for i := uint8(6); i <= 28; i++ {
		if u.featureGet(1 << i) {
			t.Error("feature should not be set")
		}
	}
	if u.isProtected() {
		t.Error("should not be protected")
	}
	if u.isBot() {
		t.Error("should not be bot")
	}
	if u.isSubscriber() {
		t.Error("should not be subscriber")
	}
	if u.isModerator() {
		t.Error("should not be moderator")
	}

	//--------
	features := []string{"admin", "moderator", "protected", "subscriber", "vip", "bot"}
	u.setFeatures(features)

	if !u.featureGet(ISPROTECTED) {
		t.Error("feature should be set")
	}
	if !u.featureGet(ISSUBSCRIBER) {
		t.Error("feature should be set")
	}
	if !u.featureGet(ISVIP) {
		t.Error("feature should be set")
	}
	if !u.featureGet(ISMODERATOR) {
		t.Error("feature should be set")
	}
	if !u.featureGet(ISADMIN) {
		t.Error("feature should be set")
	}
	if !u.featureGet(ISBOT) {
		t.Error("feature should be set")
	}
	for i := uint8(6); i <= 28; i++ {
		if u.featureGet(1 << i) {
			t.Error("feature should not be set")
		}
	}
	if !u.isProtected() {
		t.Error("should be protected")
	}
	if !u.isBot() {
		t.Error("should be bot")
	}
	if !u.isSubscriber() {
		t.Error("should be subscriber")
	}
	if !u.isModerator() {
		t.Error("should be moderator")
	}
}

func TestParseJwt(t *testing.T) {
	oldSecret := JWTSECRET
	JWTSECRET = "test-secret"
	defer func() { JWTSECRET = oldSecret }()

	const id = "75362df9-9319-4cd2-991a-fab94a9f544a"

	sign := func(method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
		t.Helper()
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatalf("signing token: %v", err)
		}
		return s
	}

	t.Run("round trip", func(t *testing.T) {
		token, err := createAPIJWT(id)
		if err != nil {
			t.Fatalf("createAPIJWT: %v", err)
		}
		claims, err := parseJwt(token)
		if err != nil {
			t.Fatalf("parseJwt: %v", err)
		}
		if claims.UserId != id {
			t.Errorf("UserId = %q, want %q", claims.UserId, id)
		}
	})

	invalid := map[string]string{
		"wrong secret": sign(jwt.SigningMethodHS256, []byte("other-secret"), jwt.MapClaims{
			"id":  id,
			"exp": time.Now().Add(time.Hour).Unix(),
		}),
		"expired": sign(jwt.SigningMethodHS256, []byte(JWTSECRET), jwt.MapClaims{
			"id":  id,
			"exp": time.Now().Add(-time.Minute).Unix(),
		}),
		"alg none": sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{
			"id":  id,
			"exp": time.Now().Add(time.Hour).Unix(),
		}),
		"garbage": "not.a.jwt",
	}
	for name, token := range invalid {
		t.Run(name, func(t *testing.T) {
			if claims, err := parseJwt(token); err == nil {
				t.Errorf("parseJwt accepted invalid token, claims = %+v", claims)
			}
		})
	}
}
