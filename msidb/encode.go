package msidb

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
	"github.com/abemedia/go-msi/internal/stringpool"
)

// encoder writes a Database to a CFB writer.
type encoder struct {
	db  *Database
	cw  *cfb.Writer
	buf []byte // reused by io.CopyBuffer
}

// encode serialises db to ws as an MSI compound file.
func encode(ws io.WriteSeeker, db *Database) error {
	e := encoder{
		db:  db,
		cw:  cfb.NewWriterV4(ws),
		buf: make([]byte, 32*1024),
	}
	e.cw.CLSID = installerCLSID

	if err := e.writePool(); err != nil {
		return err
	}
	if err := e.writeSchemas(); err != nil {
		return err
	}
	if err := e.writeTables(); err != nil {
		return err
	}
	if err := e.writeStreams(); err != nil {
		return err
	}
	return e.cw.Close()
}

// writePool emits _StringPool and _StringData.
func (e *encoder) writePool() error {
	poolData, dataData, err := stringpool.Encode(e.db.pool)
	if err != nil {
		return fmt.Errorf("pool encode: %w", err)
	}
	if err := e.writeTableStream(stringpoolName, poolData); err != nil {
		return err
	}
	return e.writeTableStream(stringdataName, dataData)
}

// writeSchemas emits _Tables and _Columns.
func (e *encoder) writeSchemas() error {
	tablesRecords := make([][]uint32, 0, len(e.db.tables))
	var columnsRecords [][]uint32
	for t := range e.db.Tables() {
		if t.name == systemTableStreams {
			continue
		}
		tableID, _ := e.db.pool.LookupID(t.name)
		tablesRecords = append(tablesRecords, []uint32{tableID})
		for j, col := range t.columns {
			colID, _ := e.db.pool.LookupID(col.Name)
			columnsRecords = append(columnsRecords, []uint32{
				tableID,
				encodeInt(j+1, 2),
				colID,
				encodeInt(int(packType(col)), 2),
			})
		}
	}

	// MSI stores _Tables and _Columns sorted by their raw primary keys: the
	// table name's string ID, then the column number.
	slices.SortFunc(tablesRecords, func(a, b []uint32) int {
		return cmp.Compare(a[0], b[0])
	})
	slices.SortFunc(columnsRecords, func(a, b []uint32) int {
		if d := cmp.Compare(a[0], b[0]); d != 0 {
			return d
		}
		return cmp.Compare(a[1], b[1])
	})

	longRefs := e.db.pool.LongRefs()
	tablesData, err := encodeTable(tablesRecords, schemaTables, longRefs)
	if err != nil {
		return fmt.Errorf("encode _Tables: %w", err)
	}
	if err := e.writeTableStream(systemTableTables, tablesData); err != nil {
		return err
	}
	columnsData, err := encodeTable(columnsRecords, schemaColumns, longRefs)
	if err != nil {
		return fmt.Errorf("encode _Columns: %w", err)
	}
	return e.writeTableStream(systemTableColumns, columnsData)
}

func (e *encoder) writeTables() error {
	longRefs := e.db.pool.LongRefs()
	for t := range e.db.Tables() {
		if t.name == systemTableStreams || len(t.records) == 0 {
			continue
		}
		records := make([][]uint32, len(t.records))
		for i, r := range t.records {
			records[i] = r.fields
		}
		stream, err := encodeTable(records, t.columns, longRefs)
		if err != nil {
			return fmt.Errorf("table %s: %w", t.name, err)
		}
		if err := e.writeTableStream(t.name, stream); err != nil {
			return fmt.Errorf("write table %s: %w", t.name, err)
		}
	}
	return nil
}

// writeStreams emits each _Streams record's Data value as a named CFB stream.
func (e *encoder) writeStreams() error {
	t, ok := e.db.tables[systemTableStreams]
	if !ok {
		return nil
	}
	for _, r := range t.records {
		name, ok := e.db.pool.Lookup(r.fields[0])
		if !ok {
			return fmt.Errorf("_Streams: unknown string ID %d", r.fields[0])
		}
		src, ok := e.db.streams[name]
		if !ok {
			continue
		}
		// Names with the '\x05' prefix (e.g. "\x05SummaryInformation") aren't encoded.
		streamName := name
		if !strings.HasPrefix(name, "\x05") {
			streamName = streamname.Encode(name)
		}
		if err := e.writeStream(streamName, src); err != nil {
			return fmt.Errorf("write stream %s: %w", name, err)
		}
	}
	return nil
}

// writeTableStream writes data as a table-namespace CFB stream called name.
func (e *encoder) writeTableStream(name string, data []byte) error {
	sw, err := e.cw.CreateStream(tableMarker + streamname.Encode(name))
	if err != nil {
		return err
	}
	if _, err := sw.Write(data); err != nil {
		sw.Close()
		return err
	}
	return sw.Close()
}

// writeStream copies src into a CFB stream called name.
func (e *encoder) writeStream(name string, src streamSource) error {
	sw, err := e.cw.CreateStream(name)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(sw, src.open(), e.buf); err != nil {
		sw.Close()
		return err
	}
	return sw.Close()
}

// encodeTable serialises per-record raw field values to a column-major stream.
func encodeTable(records [][]uint32, schema []Column, longRefs bool) ([]byte, error) {
	widths, recordSize := columnWidths(schema, longRefs)
	stream := make([]byte, len(records)*recordSize)
	pos := 0
	for c, col := range schema {
		w := widths[c]
		for r, rec := range records {
			raw := rec[c]
			if raw == 0 {
				continue
			}
			if w < 4 && raw>>(8*w) != 0 {
				return nil, fmt.Errorf("record %d col %s: value %#x does not fit %d bytes", r, col.Name, raw, w)
			}
			dst := stream[pos+r*w : pos+(r+1)*w]
			switch w {
			case 2:
				binary.LittleEndian.PutUint16(dst, uint16(raw))
			case 4:
				binary.LittleEndian.PutUint32(dst, raw)
			default:
				dst[0], dst[1], dst[2] = byte(raw), byte(raw>>8), byte(raw>>16)
			}
		}
		pos += len(records) * w
	}
	return stream, nil
}

// encodeInt returns the raw size-byte integer field value for v.
func encodeInt(v, size int) uint32 {
	if size == 2 {
		return uint32(uint16(v) ^ 0x8000)
	}
	return uint32(v) ^ 0x80000000
}
