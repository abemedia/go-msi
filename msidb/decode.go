package msidb

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
	"github.com/abemedia/go-msi/internal/stringpool"
)

// decode parses r into a Database. Any damage fails with [ErrFormat].
func decode(r *cfb.Reader) (*Database, error) {
	db := newDatabase()
	db.clsid = r.CLSID

	tableStreams := make(map[string]*cfb.Stream, len(r.Entries))
	var dataStreams []*cfb.Stream
	for _, e := range r.Entries {
		switch e := e.(type) {
		case *cfb.Storage:
			db.storages = append(db.storages, e)
		case *cfb.Stream:
			if strings.HasPrefix(e.Name, tableMarker) {
				tableStreams[e.Name] = e
			} else {
				dataStreams = append(dataStreams, e)
			}
		}
	}

	if err := readPool(db, tableStreams); err != nil {
		return nil, err
	}
	if err := readCatalog(db, tableStreams); err != nil {
		return nil, err
	}
	if err := readTables(db, tableStreams); err != nil {
		return nil, err
	}
	if err := readStreams(db, dataStreams); err != nil {
		return nil, err
	}
	return db, nil
}

func readPool(db *Database, tableStreams map[string]*cfb.Stream) error {
	poolStream, okPool := tableStreams[tableStreamName(stringPoolName)]
	dataStream, okData := tableStreams[tableStreamName(stringDataName)]
	if !okPool || !okData {
		return fmt.Errorf("%w: missing string pool", ErrFormat)
	}
	delete(tableStreams, tableStreamName(stringPoolName))
	delete(tableStreams, tableStreamName(stringDataName))
	poolBytes, err := readAll(poolStream)
	if err != nil {
		return fmt.Errorf("string pool: %w", err)
	}
	dataBytes, err := readAll(dataStream)
	if err != nil {
		return fmt.Errorf("string pool: %w", err)
	}
	if db.pool, err = stringpool.Decode(poolBytes, dataBytes); err != nil {
		return fmt.Errorf("%w: string pool: %w", ErrFormat, err)
	}
	return nil
}

func readCatalog(db *Database, tableStreams map[string]*cfb.Stream) error {
	for _, name := range []string{systemTableTables, systemTableColumns} {
		s, ok := tableStreams[tableStreamName(name)]
		if !ok {
			continue
		}
		delete(tableStreams, tableStreamName(name))
		t := db.tables[name]
		data, err := readAll(s)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrFormat, name, err)
		}
		if t.records, err = decodeTable(db, data, t.cols); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrFormat, name, err)
		}
		slices.SortStableFunc(t.records, t.compareKeys)
	}

	// Anything the file holds is persistent, whatever its bit says.
	for _, rec := range db.tables[systemTableColumns].records {
		if rec[columnsType] != 0 {
			rec[columnsType] |= typePersistent
		}
	}
	return nil
}

func readTables(db *Database, tableStreams map[string]*cfb.Stream) error {
	for _, rec := range db.tables[systemTableTables].records {
		name, _ := db.pool.Lookup(rec[0])
		if name == "" {
			return fmt.Errorf("%w: _Tables record has no name", ErrFormat)
		}
		if isReservedName(name) {
			return fmt.Errorf("%w: _Tables record for reserved name %q", ErrFormat, name)
		}
		if _, ok := db.tables[name]; ok {
			return fmt.Errorf("%w: table %s has more than one _Tables record", ErrFormat, name)
		}
		t, err := db.deriveTable(name)
		if err != nil {
			return fmt.Errorf("%w: table %s: %w", ErrFormat, name, err)
		}
		db.tables[name] = t
		s, ok := tableStreams[tableStreamName(name)]
		if !ok {
			continue
		}
		delete(tableStreams, tableStreamName(name))
		data, err := readAll(s)
		if err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
		if t.records, err = decodeTable(db, data, t.cols); err != nil {
			return fmt.Errorf("%w: table %s: %w", ErrFormat, name, err)
		}
		if len(t.keys) > 0 {
			dup := false
			slices.SortFunc(t.records, func(a, b []uint32) int {
				d := t.compareKeys(a, b)
				dup = dup || d == 0
				return d
			})
			if dup {
				return fmt.Errorf("%w: table %s: duplicate primary key", ErrFormat, name)
			}
		}
	}
	for _, rec := range db.tables[systemTableColumns].records {
		name, _ := db.pool.Lookup(rec[columnsTable])
		if _, ok := db.tables[name]; !ok || isReservedName(name) {
			return fmt.Errorf("%w: _Columns record for %q has no _Tables record", ErrFormat, name)
		}
	}
	for name := range tableStreams {
		name = streamname.Decode(strings.TrimPrefix(name, tableMarker))
		return fmt.Errorf("%w: table stream %s has no _Tables record", ErrFormat, name)
	}
	return nil
}

// readStreams registers each data stream in _Streams under its decoded name.
func readStreams(db *Database, dataStreams []*cfb.Stream) error {
	st := db.tables[systemTableStreams]
	for _, s := range dataStreams {
		name := streamname.Decode(s.Name)
		id := db.pool.Intern(name, false)
		if old, dup := db.sources[id]; dup {
			return fmt.Errorf("%w: streams %q and %q both decode to %q", ErrFormat, old.(*cfbStreamSource).s.Name, s.Name, name)
		}
		st.records = append(st.records, []uint32{id, 1})
		db.sources[id] = &cfbStreamSource{s: s}
	}
	slices.SortStableFunc(st.records, st.compareKeys)
	return nil
}

// decodeTable parses a table stream, checking string cells against the pool.
func decodeTable(db *Database, data []byte, schema []Column) ([][]uint32, error) {
	records, err := decodeRecords(data, schema, db.pool.LongRefs())
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		for i, c := range schema {
			if c.Type == ColumnString && rec[i] != 0 {
				if _, ok := db.pool.Lookup(rec[i]); !ok {
					return nil, fmt.Errorf("unknown string id %d in column %q", rec[i], c.Name)
				}
			}
		}
	}
	return records, nil
}

// decodeRecords parses a column-major table stream to per-record raw cells.
func decodeRecords(stream []byte, schema []Column, longRefs bool) ([][]uint32, error) {
	widths, recordSize := columnWidths(schema, longRefs)
	if recordSize == 0 {
		return nil, nil
	}
	if len(stream)%recordSize != 0 {
		return nil, fmt.Errorf("stream length %d not a multiple of record size %d", len(stream), recordSize)
	}
	recordCount := len(stream) / recordSize
	if recordCount == 0 {
		return nil, nil
	}
	flat := make([]uint32, recordCount*len(schema))
	out := make([][]uint32, recordCount)
	for r := range out {
		out[r] = flat[r*len(schema):][:len(schema):len(schema)]
	}
	pos := 0
	for c := range schema {
		w := widths[c]
		for r := range recordCount {
			b := stream[pos+r*w : pos+(r+1)*w]
			switch w {
			case 2:
				out[r][c] = uint32(binary.LittleEndian.Uint16(b))
			case 4:
				out[r][c] = binary.LittleEndian.Uint32(b)
			default: // w == 3
				out[r][c] = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
			}
		}
		pos += recordCount * w
	}
	return out, nil
}

// readAll returns the full contents of s, or nil if s is nil or zero-length.
func readAll(s *cfb.Stream) ([]byte, error) {
	if s == nil || s.Size == 0 {
		return nil, nil
	}
	buf := make([]byte, s.Size)
	if _, err := io.ReadFull(s.Open(), buf); err != nil {
		return nil, err
	}
	return buf, nil
}
