package msidb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
	"github.com/abemedia/go-msi/internal/stringpool"
)

type encoder struct {
	db  *Database
	cw  *cfb.Writer
	buf []byte // reused by io.CopyBuffer
}

func encode(ws io.WriteSeeker, db *Database) error {
	e := encoder{
		db:  db,
		cw:  cfb.NewWriterV4(ws),
		buf: make([]byte, 32*1024),
	}
	e.cw.CLSID = db.clsid

	if err := e.writePool(); err != nil {
		return err
	}
	if err := e.writeCatalog(); err != nil {
		return err
	}
	if err := e.writeTables(); err != nil {
		return err
	}
	if err := e.writeStreams(); err != nil {
		return err
	}
	if err := e.writeStorages(); err != nil {
		return err
	}
	return e.cw.Close()
}

func (e *encoder) writePool() error {
	poolData, dataData, err := stringpool.Encode(e.db.pool)
	if err != nil {
		return fmt.Errorf("pool encode: %w", err)
	}
	if err := e.writeTableStream(stringPoolName, poolData); err != nil {
		return err
	}
	return e.writeTableStream(stringDataName, dataData)
}

func (e *encoder) writeCatalog() error {
	longRefs := e.db.pool.LongRefs()

	tt := e.db.tables[systemTableTables]
	tablesRecs := make([][]uint32, 0, len(tt.records))
	for _, rec := range tt.records {
		if e.db.tableRecordPersistent(rec[0]) {
			tablesRecs = append(tablesRecs, rec)
		}
	}
	if err := e.writeTableStream(systemTableTables, encodeRecords(tablesRecs, schemaTables, longRefs)); err != nil {
		return err
	}

	ct := e.db.tables[systemTableColumns]
	columnsRecs := make([][]uint32, 0, len(ct.records))
	for _, rec := range ct.records {
		if rec[columnsType]&typePersistent != 0 {
			columnsRecs = append(columnsRecs, rec)
		}
	}
	if len(columnsRecs) == 0 {
		return nil // An empty _Columns has no stream.
	}
	return e.writeTableStream(systemTableColumns, encodeRecords(columnsRecs, schemaColumns, longRefs))
}

// writeTables emits each persistent table's persistent records as a table stream.
func (e *encoder) writeTables() error {
	longRefs := e.db.pool.LongRefs()
	names := make([]string, 0, len(e.db.tables))
	for name, t := range e.db.tables {
		if isSystemTable(name) || !t.persistent() || len(t.records) == 0 {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		t := e.db.tables[name]
		recs := t.records
		if len(t.temp) > 0 {
			recs = make([][]uint32, 0, len(t.records))
			for _, rec := range t.records {
				if _, temp := t.temp[&rec[0]]; !temp {
					recs = append(recs, rec)
				}
			}
		}
		if len(recs) == 0 {
			continue
		}
		stream := encodeRecords(recs, persistentPrefix(t.cols), longRefs)
		if err := e.writeTableStream(name, stream); err != nil {
			return fmt.Errorf("write table %s: %w", name, err)
		}
	}
	return nil
}

// writeStreams emits each data stream in _Streams record order. Names with
// the '\x05' prefix are written verbatim, not encoded.
func (e *encoder) writeStreams() error {
	st := e.db.tables[systemTableStreams]
	for _, rec := range st.records {
		name, ok := e.db.pool.Lookup(rec[0])
		if !ok {
			return fmt.Errorf("_Streams: unknown string id %d", rec[0])
		}
		src, ok := e.db.sources[rec[0]]
		if !ok {
			return fmt.Errorf("_Streams: no source for %q", name)
		}
		streamName := name
		if !strings.HasPrefix(name, "\x05") {
			streamName = streamname.Encode(name)
		}
		if err := e.writeStream(streamName, src.open()); err != nil {
			return fmt.Errorf("write stream %s: %w", name, err)
		}
	}
	return nil
}

func (e *encoder) writeStorages() error {
	for _, s := range e.db.storages {
		if err := e.writeStorage(e.cw.StorageWriter, s); err != nil {
			return fmt.Errorf("write storage %s: %w", s.Name, err)
		}
	}
	return nil
}

func (e *encoder) writeStorage(parent *cfb.StorageWriter, s *cfb.Storage) error {
	sw, err := parent.CreateStorage(s.Name)
	if err != nil {
		return err
	}
	sw.CLSID, sw.StateBits, sw.Created, sw.Modified = s.CLSID, s.StateBits, s.Created, s.Modified
	for _, entry := range s.Entries {
		switch entry := entry.(type) {
		case *cfb.Storage:
			if err := e.writeStorage(sw, entry); err != nil {
				return err
			}
		case *cfb.Stream:
			w, err := sw.CreateStream(entry.Name)
			if err != nil {
				return err
			}
			w.StateBits = entry.StateBits
			if _, err := io.CopyBuffer(w, entry.Open(), e.buf); err != nil {
				w.Close()
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *encoder) writeTableStream(name string, data []byte) error {
	return e.writeStream(tableStreamName(name), bytes.NewReader(data))
}

func (e *encoder) writeStream(name string, r io.Reader) error {
	sw, err := e.cw.CreateStream(name)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(sw, r, e.buf); err != nil {
		sw.Close()
		return err
	}
	return sw.Close()
}

// encodeRecords serialises per-record raw cells to a column-major stream.
func encodeRecords(records [][]uint32, schema []Column, longRefs bool) []byte {
	widths, recordSize := columnWidths(schema, longRefs)
	stream := make([]byte, len(records)*recordSize)
	pos := 0
	for c := range schema {
		w := widths[c]
		for r, rec := range records {
			raw := rec[c]
			if raw == 0 {
				continue
			}
			dst := stream[pos+r*w : pos+(r+1)*w]
			switch w {
			case 2:
				binary.LittleEndian.PutUint16(dst, uint16(raw))
			case 4:
				binary.LittleEndian.PutUint32(dst, raw)
			default: // w == 3
				dst[0], dst[1], dst[2] = byte(raw), byte(raw>>8), byte(raw>>16)
			}
		}
		pos += len(records) * w
	}
	return stream
}
