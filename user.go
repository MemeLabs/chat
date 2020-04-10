package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
)

type userTools struct {
	nicklookup  map[string]*uidprot
	nicklock    sync.RWMutex
	featurelock sync.RWMutex
	features    map[uint32][]string
}

var usertools = userTools{nicklookup: make(map[string]*uidprot), nicklock: sync.RWMutex{}, featurelock: sync.RWMutex{}, features: make(map[uint32][]string)}

const (
	ISADMIN      = 1 << iota
	ISMODERATOR  = 1 << iota
	ISVIP        = 1 << iota
	ISPROTECTED  = 1 << iota
	ISSUBSCRIBER = 1 << iota
	ISBOT        = 1 << iota
)

type uidprot struct {
	id        Userid
	protected bool
}

func (ut *userTools) getUseridForNick(nick string) (Userid, bool) {
	ut.nicklock.RLock()
	d, ok := uidprot{}, false // ut.nicklookup[strings.ToLower(nick)] //TODO reimplement...
	if !ok {
		uid, protected := db.getUser(nick)
		if uid != 0 {
			ut.nicklock.RUnlock()
			ut.nicklock.Lock()
			ut.nicklookup[strings.ToLower(nick)] = &uidprot{uid, protected}
			ut.nicklock.Unlock()
			return uid, protected
		}
		ut.nicklock.RUnlock()
		return 0, false
	}
	ut.nicklock.RUnlock()
	return d.id, d.protected
}

func (ut *userTools) addUser(u *User, force bool) {
	lowernick := strings.ToLower(u.nick)
	if !force {
		ut.nicklock.RLock()
		_, ok := ut.nicklookup[lowernick]
		ut.nicklock.RUnlock()
		if ok {
			return
		}
	}
	ut.nicklock.Lock()
	defer ut.nicklock.Unlock()
	ut.nicklookup[lowernick] = &uidprot{u.id, u.isProtected()}
}

type Userid int32

type User struct {
	id              Userid
	nick            string
	features        uint32
	lastmessage     []byte // TODO remove?
	lastmessagetime time.Time
	delayscale      uint8
	simplified      *SimplifiedUser
	connections     int32
	sync.RWMutex
}

type UserClaims struct {
	UserId string `json:"id"` // TODO from rustla2 backend impl
	jwt.StandardClaims
}

// TODO
func parseJwt(cookie string) (*UserClaims, error) {
	// verify jwt cookie - https://godoc.org/github.com/dgrijalva/jwt-go#example-Parse--Hmac
	token, err := jwt.ParseWithClaims(cookie, &UserClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(JWTSECRET), nil
	})
	if err != nil {
		return nil, errors.New("Token invalid")
	}

	if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
		return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
	}

	claims, ok := token.Claims.(*UserClaims) // TODO

	if !ok || !token.Valid {
		return nil, errors.New("Token invalid")
	}

	return claims, nil
}

// TODO
func userFromAPI(uuid string) (username string, err error) {
	// TODO here we trusted signed id in claims json is well-formed uuid...

	// TODO check exp-time as the backend does! (or not?) -- {"id":"uuid","exp":futurts}

	if err != nil {
		fmt.Println("err1", uuid)
		return "", err
	}

	// TODO - get username from api
	type un struct {
		Username string `json:"username"`
	}

	resp, err := http.Get(fmt.Sprintf("%s%s", USERNAMEAPI, uuid))
	if err != nil {
		return "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("err2", err)
		return "", err
	}

	response := un{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		fmt.Println("err3", err)
		return "", err
	}

	D("username parsed:", response)
	if response.Username == "" {
		return "", errors.New("User needs to set a username")
	}

	return response.Username, nil
}

func userfromCookie(cookie string, ip string) (u *User, err error) {
	// TODO remoteaddr in go contains port - now we use the header that doesnt... TODO standardize...
	// ip = strings.Split(ip, ":")[0]

	claims, err := parseJwt(cookie)
	if err != nil {
		return nil, err
	}

	username, err := userFromAPI(claims.UserId)
	if err != nil {
		return nil, err
	}

	// if user not found, insert new user into db

	// ignoring the error for now
	db.newUser(claims.UserId, username, ip)
	// TODO err is expected for non-new users...

	// now get features from db, update stuff - TODO

	features, uid, err := db.getUserInfo(claims.UserId)
	if err != nil {
		fmt.Println("err4", err)
		return nil, err
	}

	// finally update records...
	db.updateUser(Userid(uid), username, ip)

	u = &User{
		id:              Userid(uid),
		nick:            username,
		features:        0,
		lastmessage:     nil,
		lastmessagetime: time.Time{},
		delayscale:      1,
		simplified:      nil,
		connections:     0,
		RWMutex:         sync.RWMutex{},
	}

	// init features finally - CASE SENSITIVE. TODO.
	u.setFeatures(features)

	forceupdate := false
	if cu := namescache.get(u.id); cu != nil && cu.features == u.features {
		forceupdate = true
	}

	u.assembleSimplifiedUser()
	usertools.addUser(u, forceupdate)
	return u, nil
}

func (u *User) featureGet(bitnum uint32) bool {
	return ((u.features & bitnum) != 0)
}

func (u *User) featureSet(bitnum uint32) {
	u.features |= bitnum
}

func (u *User) featureCount() (c uint8) {
	// Counting bits set, Brian Kernighan's way
	v := u.features
	for c = 0; v != 0; c++ {
		v &= v - 1 // clear the least significant bit set
	}
	return
}

// isModerator checks if the user can use mod commands
func (u *User) isModerator() bool {
	return u.featureGet(ISMODERATOR | ISADMIN)
}

// isSubscriber checks if the user can speak when the chat is in submode
func (u *User) isSubscriber() bool {
	return u.featureGet(ISSUBSCRIBER | ISADMIN | ISMODERATOR | ISVIP | ISBOT)
}

// isBot checks if the user is exempt from ratelimiting
func (u *User) isBot() bool {
	return u.featureGet(ISBOT)
}

// isProtected checks if the user can be moderated or not
func (u *User) isProtected() bool {
	return u.featureGet(ISADMIN | ISPROTECTED)
}

func (u *User) setFeatures(features []string) {
	for _, feature := range features {
		switch feature {
		case "admin":
			u.featureSet(ISADMIN)
		case "moderator":
			u.featureSet(ISMODERATOR)
		case "protected":
			u.featureSet(ISPROTECTED)
		case "subscriber":
			u.featureSet(ISSUBSCRIBER)
		case "vip":
			u.featureSet(ISVIP)
		case "bot":
			u.featureSet(ISBOT)
		case "":
			continue
		default: // flairNN for future flairs
			if feature[:5] == "flair" {
				flair, err := strconv.Atoi(feature[5:])
				if err != nil {
					D("Could not parse unknown feature:", feature, err)
					continue
				}
				// six proper features, all others are just useless flairs
				u.featureSet(1 << (6 + uint8(flair)))
			}
		}
	}
}

func (u *User) assembleSimplifiedUser() {
	usertools.featurelock.RLock()
	f, ok := usertools.features[u.features]
	usertools.featurelock.RUnlock()

	if !ok {
		usertools.featurelock.Lock()
		defer usertools.featurelock.Unlock()

		numfeatures := u.featureCount()
		f = make([]string, 0, numfeatures)
		if u.featureGet(ISPROTECTED) {
			f = append(f, "protected")
		}
		if u.featureGet(ISSUBSCRIBER) {
			f = append(f, "subscriber")
		}
		if u.featureGet(ISVIP) {
			f = append(f, "vip")
		}
		if u.featureGet(ISMODERATOR) {
			f = append(f, "moderator")
		}
		if u.featureGet(ISADMIN) {
			f = append(f, "admin")
		}
		if u.featureGet(ISBOT) {
			f = append(f, "bot")
		}

		for i := uint8(6); i <= 26; i++ {
			if u.featureGet(1 << i) {
				flair := fmt.Sprintf("flair%d", i-6)
				f = append(f, flair)
			}
		}

		usertools.features[u.features] = f
	}

	u.simplified = &SimplifiedUser{
		u.nick,
		&f,
	}
}

func getUserFromWebRequest(r *http.Request) (user *User, banned bool, ip string) {
	// TODO make this an option? - need this if run behind e.g. nginx
	// TODO test
	ip = r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}

	ip = getMaskedIP(ip)
	banned = bans.isIPBanned(ip)
	if banned {
		return
	}

	jwtcookie, err := r.Cookie(JWTCOOKIENAME)
	if err != nil {
		return
	}

	user, err = userfromCookie(jwtcookie.Value, ip)
	if err != nil || user == nil {
		B(err)
		return
	}

	banned = bans.isUseridBanned(user.id)
	if banned {
		return
	}

	// there is only ever one single "user" struct, the namescache makes sure of that
	user = namescache.add(user)
	return
}
