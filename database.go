package main

import (
	"database/sql"
	"io/ioutil"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

type database struct {
	db        *sqlx.DB
	insertban chan *dbInsertBan
	deleteban chan *dbDeleteBan
	sync.Mutex
}

type dbInsertBan struct {
	uid       Userid
	targetuid Userid
	ipaddress *sql.NullString
	reason    string
	starttime int64
	endtime   int64
	retries   uint8
}

type dbDeleteBan struct {
	uid Userid
}

var db = &database{
	insertban: make(chan *dbInsertBan, 10),
	deleteban: make(chan *dbDeleteBan, 10),
}

func initDatabase(dbfile string, init bool) {
	db.db = sqlx.MustConnect("sqlite3", dbfile)
	if init {
		sql, err := ioutil.ReadFile("db-init.sql")
		if err != nil {
			panic(err)
		}

		stmt, err := db.db.Prepare(string(sql))
		if err != nil {
			panic(err)
		}

		stmt.Exec()
		stmt.Close()
	}

	bans.loadActive()
	go db.runInsertBan() // TODO ???
	go db.runDeleteBan()
}

func (db *database) getStatement(name string, sql string) *sql.Stmt {
	db.Lock()
	stmt, err := db.db.Prepare(sql)
	db.Unlock()
	if err != nil {
		D("Unable to create", name, "statement:", err)
		time.Sleep(100 * time.Millisecond)
		return db.getStatement(name, sql)
	}
	return stmt
}

func (db *database) getInsertBanStatement() *sql.Stmt {
	return db.getStatement("insertBan", `
		INSERT INTO bans
		VALUES (
			?,
			?,
			?,
			?,
			?,
			?
	)`)
}

func (db *database) getDeleteBanStatement() *sql.Stmt {
	return db.getStatement("deleteBan", `
		UPDATE bans
		SET endtimestamp = strftime('%s', 'now')
		WHERE
			targetuserid = ? AND
			(
				endtimestamp IS NULL OR
				endtimestamp > strftime('%s', 'now')
			)
	`)
}

func (db *database) runInsertBan() {
	t := time.NewTimer(time.Minute)
	stmt := db.getInsertBanStatement()
	for {
		select {
		case <-t.C:
			stmt.Close()
			stmt = nil
		case data := <-db.insertban:
			t.Reset(time.Minute)
			if stmt == nil {
				stmt = db.getInsertBanStatement()
			}
			if data.retries > 2 {
				continue
			}
			db.Lock()
			_, err := stmt.Exec(data.uid, data.targetuid, data.ipaddress, data.reason, data.starttime, data.endtime)
			db.Unlock()
			if err != nil {
				data.retries++
				D("Unable to insert event", err)
				go (func() {
					db.insertban <- data
				})()
			}
		}
	}
}

func (db *database) runDeleteBan() {
	t := time.NewTimer(time.Minute)
	stmt := db.getDeleteBanStatement()
	for {
		select {
		case <-t.C:
			stmt.Close()
			stmt = nil
		case data := <-db.deleteban:
			t.Reset(time.Minute)
			if stmt == nil {
				stmt = db.getDeleteBanStatement()
			}
			db.Lock()
			_, err := stmt.Exec(data.uid)
			db.Unlock()
			if err != nil {
				D("Unable to insert event", err)
				go (func() {
					db.deleteban <- data
				})()
			}
		}
	}
}

func (db *database) insertBan(uid Userid, targetuid Userid, ban *BanIn, ip string) {
	ipaddress := &sql.NullString{}
	if ban.BanIP && len(ip) != 0 {
		ipaddress.String = ip
		ipaddress.Valid = true
	}

	starttime := time.Now().UTC()
	var endtimestamp int64

	if ban.Ispermanent {
		endtimestamp = getFuturetimeUTC().Unix()
	} else {
		endtimestamp = starttime.Add(time.Duration(ban.Duration)).Unix()
	}

	starttimestamp := starttime.Unix()

	db.insertban <- &dbInsertBan{uid, targetuid, ipaddress, ban.Reason, starttimestamp, endtimestamp, 0}
}

func (db *database) deleteBan(targetuid Userid) {
	db.deleteban <- &dbDeleteBan{targetuid}
}

func (db *database) getBans(f func(Userid, sql.NullString, time.Time)) {
	db.Lock()
	defer db.Unlock()

	rows, err := db.db.Query(`
		SELECT
			targetuserid,
			ipaddress,
			endtimestamp
		FROM bans
		WHERE
			endtimestamp IS NULL OR
			endtimestamp > strftime('%s', 'now')
		GROUP BY targetuserid, ipaddress
	`)
	if err != nil {
		D("Unable to get active bans: ", err)
		return
	}

	defer rows.Close()
	for rows.Next() {
		var uid Userid
		var ipaddress sql.NullString
		var endtimestamp time.Time
		var t int64
		err = rows.Scan(&uid, &ipaddress, &t)
		if err != nil {
			D("Unable to scan bans row: ", err)
			continue
		}

		endtimestamp = time.Unix(t, 0).UTC()

		f(uid, ipaddress, endtimestamp)
	}
}

func (db *database) getUser(nick string) (Userid, bool) {
	stmt := db.getStatement("getUser", `
		SELECT
			u.userid,
			instr(u.features, 'admin')
		FROM users AS u
		WHERE u.nick = ?
	`)
	db.Lock()
	defer stmt.Close()
	defer db.Unlock()

	var uid int32
	var protected bool
	err := stmt.QueryRow(nick).Scan(&uid, &protected)
	if err != nil {
		D("error looking up", nick, err)
		return 0, false
	}
	return Userid(uid), protected
}

// TODO ... for uuid-id conversion
func (db *database) getUserInfo(uuid string) ([]string, int, error) {
	stmt := db.getStatement("getUserInfo", `
		SELECT
			userid, features
		FROM users
		WHERE uuid = ?
	`)
	db.Lock()
	defer stmt.Close()
	defer db.Unlock()

	var f string
	var uid int
	err := stmt.QueryRow(uuid).Scan(&uid, &f)
	if err != nil {
		D("features err", err)
		return []string{}, -1, err // TODO -1 implications...
	}
	features := strings.Split(f, ",") // TODO features are placed into db like this...
	return features, uid, nil
}

func (db *database) newUser(uuid string, name string, ip string) error {
	// TODO
	// chat-internal uid is autoincrement primary key...
	// UNIQUE check on uuid makes sure of no double intserts.
	stmt := db.getStatement("newUser", `
		INSERT INTO users (
			uuid, nick, features, firstlogin, lastlogin, lastip
		)
		VALUES (
			?, ?, "", strftime('%s', 'now'), strftime('%s', 'now'), ?
		)
	`)

	db.Lock()
	defer stmt.Close()
	defer db.Unlock()

	_, err := stmt.Exec(uuid, name, ip)
	if err != nil {
		D("newuser err", err) // TODO this is actually expected and normal for existing users...
		return err
	}

	return nil
}

func (db *database) updateUser(id Userid, name string, ip string) error {
	stmt := db.getStatement("updateUser", `
		UPDATE users SET 
			nick = ?,
			lastlogin = strftime('%s', 'now'),
			lastip = ?
		WHERE userid = ?
	`)
	db.Lock()
	defer stmt.Close()
	defer db.Unlock()

	_, err := stmt.Exec(name, ip, id)
	if err != nil {
		D("updateUser err", err)
		return err
	}

	return nil
}
