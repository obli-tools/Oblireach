package main

// sqlite_read.go — a minimal READ-ONLY SQLite reader, enough to list the rows
// of one ordinary (rowid) table of a database file: what the 1Remote import
// needs (its "Servers" table, connimport_1remote.go). No dependency (the app
// has no CGo and the pure-Go SQLite drivers are large): the file format is
// stable and documented (https://www.sqlite.org/fileformat2.html).
//
// Supported: UTF-8 databases, any page size, table b-trees (interior 0x05,
// leaf 0x0D), overflow pages, sqlite_schema on page 1. Not supported (an
// error, never a wrong answer): UTF-16 databases, WITHOUT ROWID tables.
// The write-ahead log (a "-wal" file next to the database) is NOT read:
// changes not yet checkpointed by the owning application are missing — the
// caller warns (wal_present). The file is read whole (callers cap its size)
// and never written.
//
// Every offset is bounds-checked, every walk is bounded (pages visited,
// depth): a corrupt or hostile file gives an error, never a panic or a loop.

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
)

var errSQLiteFormat = errors.New("sqlite: unsupported or corrupt file")

type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
	pages    int
}

func openSQLite(data []byte) (*sqliteDB, error) {
	if len(data) < 100 || string(data[:16]) != "SQLite format 3\x00" {
		return nil, errSQLiteFormat
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return nil, errSQLiteFormat
	}
	reserved := int(data[20])
	if enc := binary.BigEndian.Uint32(data[56:60]); enc != 0 && enc != 1 {
		return nil, errSQLiteFormat // UTF-16
	}
	db := &sqliteDB{data: data, pageSize: ps, usable: ps - reserved, pages: len(data) / ps}
	if db.usable < 480 || db.pages < 1 {
		return nil, errSQLiteFormat
	}
	return db, nil
}

func (db *sqliteDB) page(n int) ([]byte, error) {
	if n < 1 || n > db.pages {
		return nil, errSQLiteFormat
	}
	return db.data[(n-1)*db.pageSize : n*db.pageSize], nil
}

// sqliteVarint: the big-endian varint (1-9 bytes).
func sqliteVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 8; i++ {
		if i >= len(b) {
			return 0, 0
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	if len(b) < 9 {
		return 0, 0
	}
	return v<<8 | uint64(b[8]), 9
}

// tableRows walks the table b-tree rooted at root, calling fn with each row's
// record payload (its columns decoded). fn returning false stops the walk.
func (db *sqliteDB) tableRows(root int, fn func(cols []interface{}) bool) error {
	visited := 0
	maxVisits := db.pages + 1
	var walk func(pg, depth int) (bool, error)
	walk = func(pg, depth int) (bool, error) {
		visited++
		if depth > 40 || visited > maxVisits {
			return false, errSQLiteFormat
		}
		p, err := db.page(pg)
		if err != nil {
			return false, err
		}
		hdr := 0
		if pg == 1 {
			hdr = 100
		}
		if len(p) < hdr+8 {
			return false, errSQLiteFormat
		}
		typ := p[hdr]
		ncells := int(binary.BigEndian.Uint16(p[hdr+3 : hdr+5]))
		hsize := 8
		if typ == 0x05 {
			hsize = 12
		} else if typ != 0x0D {
			return false, errSQLiteFormat // (an index or WITHOUT ROWID b-tree)
		}
		if len(p) < hdr+hsize+2*ncells {
			return false, errSQLiteFormat
		}
		for i := 0; i < ncells; i++ {
			off := int(binary.BigEndian.Uint16(p[hdr+hsize+2*i:]))
			if off < hdr+hsize || off >= db.usable {
				return false, errSQLiteFormat
			}
			if typ == 0x05 {
				if off+4 > len(p) {
					return false, errSQLiteFormat
				}
				child := int(binary.BigEndian.Uint32(p[off:]))
				if cont, err := walk(child, depth+1); err != nil || !cont {
					return cont, err
				}
				continue
			}
			payload, err := db.leafPayload(p, off)
			if err != nil {
				return false, err
			}
			cols, err := sqliteRecord(payload)
			if err != nil {
				return false, err
			}
			if !fn(cols) {
				return false, nil
			}
		}
		if typ == 0x05 {
			right := int(binary.BigEndian.Uint32(p[hdr+8:]))
			return walk(right, depth+1)
		}
		return true, nil
	}
	_, err := walk(root, 0)
	return err
}

// leafPayload: a table leaf cell's whole payload (its overflow chain read).
func (db *sqliteDB) leafPayload(p []byte, off int) ([]byte, error) {
	size, n := sqliteVarint(p[off:])
	if n == 0 || size > uint64(len(db.data)) {
		return nil, errSQLiteFormat
	}
	off += n
	_, n = sqliteVarint(p[off:]) // rowid
	if n == 0 {
		return nil, errSQLiteFormat
	}
	off += n
	P := int(size)
	U := db.usable
	X := U - 35
	local := P
	if P > X {
		M := ((U-12)*32)/255 - 23
		K := M + (P-M)%(U-4)
		if K <= X {
			local = K
		} else {
			local = M
		}
	}
	if off+local > len(p) {
		return nil, errSQLiteFormat
	}
	out := make([]byte, 0, P)
	out = append(out, p[off:off+local]...)
	if local == P {
		return out, nil
	}
	if off+local+4 > len(p) {
		return nil, errSQLiteFormat
	}
	next := int(binary.BigEndian.Uint32(p[off+local:]))
	for hops := 0; len(out) < P; hops++ {
		if next == 0 || hops > db.pages {
			return nil, errSQLiteFormat
		}
		op, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = int(binary.BigEndian.Uint32(op[0:4]))
		take := U - 4
		if rest := P - len(out); rest < take {
			take = rest
		}
		out = append(out, op[4:4+take]...)
	}
	return out, nil
}

// sqliteRecord decodes a record: int64, float64, string (text), []byte
// (blob) or nil per column.
func sqliteRecord(b []byte) ([]interface{}, error) {
	hlen, n := sqliteVarint(b)
	if n == 0 || hlen > uint64(len(b)) || int(hlen) < n {
		return nil, errSQLiteFormat
	}
	var types []uint64
	for p := n; p < int(hlen); {
		t, m := sqliteVarint(b[p:int(hlen)])
		if m == 0 {
			return nil, errSQLiteFormat
		}
		types = append(types, t)
		p += m
	}
	body := b[hlen:]
	var cols []interface{}
	for _, t := range types {
		var size int
		switch {
		case t == 0, t == 8, t == 9:
			size = 0
		case t >= 1 && t <= 4:
			size = int(t)
		case t == 5:
			size = 6
		case t == 6, t == 7:
			size = 8
		case t >= 12:
			if (t-12)/2 > uint64(len(body)) {
				return nil, errSQLiteFormat
			}
			size = int((t - 12) / 2)
		default:
			return nil, errSQLiteFormat
		}
		if size > len(body) {
			return nil, errSQLiteFormat
		}
		v := body[:size]
		body = body[size:]
		switch {
		case t == 0:
			cols = append(cols, nil)
		case t == 8:
			cols = append(cols, int64(0))
		case t == 9:
			cols = append(cols, int64(1))
		case t <= 6:
			var x int64
			if v[0]&0x80 != 0 {
				x = -1
			}
			for _, c := range v {
				x = x<<8 | int64(c)
			}
			cols = append(cols, x)
		case t == 7:
			cols = append(cols, math.Float64frombits(binary.BigEndian.Uint64(v)))
		case t%2 == 0:
			cols = append(cols, append([]byte(nil), v...))
		default:
			cols = append(cols, string(v))
		}
	}
	return cols, nil
}

// sqliteTable: the root page and the column names (from its CREATE TABLE
// text) of table name (case-insensitive).
func (db *sqliteDB) sqliteTable(name string) (root int, columns []string, err error) {
	found := false
	err = db.tableRows(1, func(c []interface{}) bool {
		if len(c) < 5 {
			return true
		}
		typ, _ := c[0].(string)
		nm, _ := c[1].(string)
		if typ != "table" || !strings.EqualFold(nm, name) {
			return true
		}
		r, _ := c[3].(int64)
		sql, _ := c[4].(string)
		root, columns, found = int(r), sqliteColumns(sql), true
		return false
	})
	if err == nil && !found {
		err = errors.New("sqlite: table not found")
	}
	return
}

// sqliteColumns: the column names of a CREATE TABLE statement (enough for
// the plain definitions an application creates: the name, then its type and
// constraints; table constraints — PRIMARY KEY(...), UNIQUE... — skipped).
func sqliteColumns(sql string) []string {
	i := strings.IndexByte(sql, '(')
	j := strings.LastIndexByte(sql, ')')
	if i < 0 || j <= i {
		return nil
	}
	body := sql[i+1 : j]
	var parts []string
	depth, start := 0, 0
	for k := 0; k < len(body); k++ {
		switch body[k] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:k])
				start = k + 1
			}
		}
	}
	parts = append(parts, body[start:])
	var cols []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var name string
		if q := p[0]; q == '`' || q == '"' || q == '[' {
			end := byte(q)
			if q == '[' {
				end = ']'
			}
			k := strings.IndexByte(p[1:], end)
			if k < 0 {
				continue
			}
			name = p[1 : k+1]
		} else {
			f := strings.Fields(p)
			name = f[0]
			switch strings.ToUpper(name) {
			case "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT":
				continue
			}
		}
		cols = append(cols, name)
	}
	return cols
}
