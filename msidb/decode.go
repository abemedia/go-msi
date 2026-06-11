package msidb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
	"github.com/abemedia/go-msi/internal/stringpool"
)

// decoder reads an MSI database from a CFB reader into a Database.
type decoder struct {
	db      *Database
	tables  map[string]*cfb.Stream
	streams []*cfb.Stream
	schemas map[string][]Column
}

// decode parses r into a Database.
func decode(r *cfb.Reader) (*Database, error) {
	d := decoder{
		db:      &Database{streams: map[string]streamSource{}},
		tables:  make(map[string]*cfb.Stream, len(r.Entries)),
		streams: make([]*cfb.Stream, 0, len(r.Entries)),
	}

	for _, e := range r.Entries {
		s, ok := e.(*cfb.Stream)
		if !ok {
			continue
		}
		if name, ok := strings.CutPrefix(s.Name, tableMarker); ok {
			d.tables[streamname.Decode(name)] = s
		} else {
			d.streams = append(d.streams, s)
		}
	}

	if err := d.readPool(); err != nil {
		return nil, err
	}
	if err := d.readSchemas(); err != nil {
		return nil, err
	}
	if err := d.readTables(); err != nil {
		return nil, err
	}
	if err := d.readStreams(); err != nil {
		return nil, err
	}
	return d.db, nil
}

// readPool decodes _StringPool and _StringData into d.db.pool.
func (d *decoder) readPool() error {
	poolStream, ok := d.tables[stringpoolName]
	if !ok {
		return errors.New("missing _StringPool stream")
	}
	dataStream, ok := d.tables[stringdataName]
	if !ok {
		return errors.New("missing _StringData stream")
	}
	poolBytes, err := readAll(poolStream)
	if err != nil {
		return fmt.Errorf("string pool: %w", err)
	}
	dataBytes, err := readAll(dataStream)
	if err != nil {
		return fmt.Errorf("string pool: %w", err)
	}
	pool, err := stringpool.Decode(poolBytes, dataBytes)
	if err != nil {
		return fmt.Errorf("string pool: %w", err)
	}
	d.db.pool = pool
	return nil
}

// readSchemas decodes _Tables and _Columns.
func (d *decoder) readSchemas() error { //nolint:funlen
	tablesStream, ok := d.tables[systemTableTables]
	if !ok {
		return errors.New("missing _Tables stream")
	}
	columnsStream, ok := d.tables[systemTableColumns]
	if !ok {
		return errors.New("missing _Columns stream")
	}
	tablesData, err := readAll(tablesStream)
	if err != nil {
		return fmt.Errorf("_Tables: %w", err)
	}
	columnsData, err := readAll(columnsStream)
	if err != nil {
		return fmt.Errorf("_Columns: %w", err)
	}
	longRefs := d.db.pool.LongRefs()
	tablesRecords, err := decodeTable(tablesData, schemaTables, longRefs)
	if err != nil {
		return fmt.Errorf("decode _Tables: %w", err)
	}
	columnsRecords, err := decodeTable(columnsData, schemaColumns, longRefs)
	if err != nil {
		return fmt.Errorf("decode _Columns: %w", err)
	}

	schemas := make(map[string][]Column, len(tablesRecords))
	for _, rec := range tablesRecords {
		id := rec[0]
		name, ok := d.db.pool.Lookup(id)
		if !ok {
			return fmt.Errorf("_Tables.Name: unknown string ID %d", id)
		}
		schemas[name] = nil
	}

	for _, rec := range columnsRecords {
		tableID, num, colID, typ := rec[0], rec[1], rec[2], rec[3]
		tableName, ok := d.db.pool.Lookup(tableID)
		if !ok {
			return fmt.Errorf("_Columns.Table: unknown string ID %d", tableID)
		}
		if num == 0 {
			return errors.New("_Columns.Number: null value")
		}
		number := decodeInt(num, 2)
		if number < 1 {
			return fmt.Errorf("_Columns.Number: invalid value %d", number)
		}
		colName, ok := d.db.pool.Lookup(colID)
		if !ok {
			return fmt.Errorf("_Columns.Name: unknown string ID %d", colID)
		}
		if typ == 0 {
			return fmt.Errorf("_Columns.Type: null value for %s.%s", tableName, colName)
		}
		col, err := unpackType(uint32(decodeInt(typ, 2)))
		if err != nil {
			return fmt.Errorf("_Columns %s.%s: %w", tableName, colName, err)
		}
		col.Name = colName

		cols, ok := schemas[tableName]
		if !ok {
			return fmt.Errorf("_Columns.Table: unknown table %q", tableName)
		}
		if number > len(cols) {
			cols = append(cols, make([]Column, number-len(cols))...)
		}
		cols[number-1] = col
		schemas[tableName] = cols
	}

	d.schemas = schemas
	d.db.tables = make(map[string]*Table, len(schemas))
	return nil
}

// readTables decodes each user table's records.
func (d *decoder) readTables() error { //nolint:funlen,gocognit
	longRefs := d.db.pool.LongRefs()
	for name, cols := range d.schemas {
		t, err := newTable(d.db, name, cols)
		if err != nil {
			return err
		}
		s, ok := d.tables[name]
		if !ok || s.Size == 0 {
			d.db.tables[name] = t
			continue
		}
		data, err := readAll(s)
		if err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
		records, err := decodeTable(data, t.columns, longRefs)
		if err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
		t.records = make([]*Record, len(records))
		for i, fields := range records {
			t.records[i] = &Record{table: t, fields: fields}
		}
		slices.SortFunc(t.records, t.comparePK)
		for ri, r := range t.records {
			if ri > 0 && len(t.primary) > 0 && t.comparePK(t.records[ri-1], r) == 0 {
				return fmt.Errorf("table %s: duplicate primary key in record %d", name, ri)
			}
			lastBin := -1
			for ci, c := range t.columns {
				fv := r.fields[ci]
				if fv == 0 {
					if !c.Nullable {
						return fmt.Errorf("table %s: record %d col %s: unexpected NULL in non-nullable column", name, ri, c.Name)
					}
					continue
				}
				switch c.Type {
				case ColumnString:
					if _, ok := t.db.pool.Lookup(fv); !ok {
						return fmt.Errorf("table %s: record %d col %s: unknown string ID %d", name, ri, c.Name, fv)
					}
				case ColumnBinary:
					if lastBin >= 0 {
						r.fields[lastBin] = 0
					}
					lastBin = ci
				}
			}
		}
		d.db.tables[name] = t
	}
	return nil
}

// readStreams synthesises the _Streams system table from every named
// CFB stream that isn't a table-content stream.
func (d *decoder) readStreams() error {
	t, err := newTable(d.db, systemTableStreams, schemaStreams)
	if err != nil {
		return err
	}
	t.records = make([]*Record, 0, len(d.streams))
	for _, s := range d.streams {
		name := streamname.Decode(s.Name)
		t.records = append(t.records, &Record{
			table:  t,
			fields: []uint32{d.db.pool.Intern(name, false), 1},
		})
		d.db.streams[name] = &cfbStreamSource{s: s}
	}
	slices.SortFunc(t.records, t.comparePK)
	d.db.tables[systemTableStreams] = t
	return nil
}

// decodeTable parses a column-major table stream into per-record raw field values.
func decodeTable(stream []byte, schema []Column, longRefs bool) ([][]uint32, error) {
	widths, recordSize := columnWidths(schema, longRefs)
	if recordSize == 0 {
		return nil, nil
	}
	if len(stream)%recordSize != 0 {
		return nil, fmt.Errorf(
			"%w: table stream length %d not a multiple of record size %d",
			ErrFormat, len(stream), recordSize,
		)
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

// decodeInt returns the value of a raw size-byte integer field value.
func decodeInt(raw uint32, size int) int {
	if size == 2 {
		return int(int16(uint16(raw) ^ 0x8000))
	}
	return int(int32(raw ^ 0x80000000))
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
