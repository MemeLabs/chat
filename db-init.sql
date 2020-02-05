
CREATE TABLE users (
    userid INTEGER PRIMARY KEY AUTOINCREMENT, /* legacy chat id */
    uuid TEXT NOT NULL UNIQUE, /* strims id */
    nick TEXT NOT NULL,
    features TEXT NOT NULL, /* array like "f1,f2" */
    firstlogin INTEGER, /* unix epoch */
    lastlogin INTEGER, /* unix epoch */
    lastip TEXT NOT NULL
);

CREATE TABLE bans (
    userid INTEGER NOT NULL, /*TODO? userid cant be uniq because deleteBan does not delete row, just update expiretime to NOW. on reban we get another ban for same id */
    targetuserid INTEGER NOT NULL, 
    ipaddress TEXT, 
    reason TEXT, 
    starttimestamp INTEGER, /* unix epoch */ 
    endtimestamp INTEGER /* unix epoch */ 
);
