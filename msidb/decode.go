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
	streams map[string]*cfb.Stream // non-table CFB streams, populated into _Streams
}

// decode parses r into a Database.
func decode(r *cfb.Reader) (*Database, error) {
	d := decoder{
		db:      &Database{},
		tables:  make(map[string]*cfb.Stream, len(r.Entries)),
		streams: make(map[string]*cfb.Stream, len(r.Entries)),
	}

	for _, e := range r.Entries {
		s, ok := e.(*cfb.Stream)
		if !ok {
			continue
		}
		decoded := streamname.Decode(s.Name)
		if name, ok := strings.CutPrefix(decoded, tableMarker); ok {
			d.tables[name] = s
		} else {
			d.streams[decoded] = s
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

// readSchemas decodes _Tables and _Columns into d.db.schemas.
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
		id := rec[0].num
		name, ok := d.db.pool.Lookup(id)
		if !ok {
			return fmt.Errorf("_Tables.Name: unknown string ID %d", id)
		}
		schemas[name] = nil
	}

	for _, rec := range columnsRecords {
		tableID := rec[0].num
		tableName, ok := d.db.pool.Lookup(tableID)
		if !ok {
			return fmt.Errorf("_Columns.Table: unknown string ID %d", tableID)
		}
		if rec[1].null {
			return errors.New("_Columns.Number: null value")
		}
		number := int(int32(rec[1].num))
		if number < 1 {
			return fmt.Errorf("_Columns.Number: invalid value %d", number)
		}
		colID := rec[2].num
		colName, ok := d.db.pool.Lookup(colID)
		if !ok {
			return fmt.Errorf("_Columns.Name: unknown string ID %d", colID)
		}
		if rec[3].null {
			return fmt.Errorf("_Columns.Type: null value for %s.%s", tableName, colName)
		}
		col, err := unpackType(rec[3].num)
		if err != nil {
			return fmt.Errorf("_Columns %s.%s: %w", tableName, colName, err)
		}
		col.Name = colName

		cols := schemas[tableName]
		for len(cols) < number {
			cols = append(cols, Column{})
		}
		cols[number-1] = col
		schemas[tableName] = cols
	}

	d.db.schemas = schemas
	d.db.tables = make(map[string]*Table, len(schemas))
	return nil
}

// readTables decodes each user table's records.
func (d *decoder) readTables() error { //nolint:gocognit
	longRefs := d.db.pool.LongRefs()
	for name, cols := range d.db.schemas {
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
			for ci, c := range t.columns {
				fv := &r.fields[ci]
				if fv.null {
					continue
				}
				switch c.Type {
				case ColumnText:
					if _, ok := t.db.pool.Lookup(fv.num); !ok {
						return fmt.Errorf("table %s: record %d col %s: unknown string ID %d", name, ri, c.Name, fv.num)
					}
				case ColumnBinary:
					streamName, err := r.binaryStreamName()
					if err != nil {
						return fmt.Errorf("table %s: %w", name, err)
					}
					s, ok := d.tables[streamName]
					if !ok {
						return fmt.Errorf("table %s: missing binary stream %q", name, streamName)
					}
					fv.bin = &cfbStreamSource{s: s}
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
	names := make([]string, 0, len(d.streams))
	for name := range d.streams {
		names = append(names, name)
	}
	slices.Sort(names)
	t.records = make([]*Record, 0, len(names))
	for _, name := range names {
		id, _ := d.db.pool.Intern(name, false)
		t.records = append(t.records, &Record{
			table:  t,
			fields: []fieldValue{{num: id}, {bin: &cfbStreamSource{s: d.streams[name]}}},
		})
	}
	slices.SortFunc(t.records, t.comparePK)
	d.db.tables[systemTableStreams] = t
	return nil
}

// decodeTable parses a column-major table stream into per-record field
// slices. Binary columns carry the on-disk presence marker only.
func decodeTable(stream []byte, schema []Column, longRefs bool) ([][]fieldValue, error) {
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
	flat := make([]fieldValue, recordCount*len(schema))
	out := make([][]fieldValue, recordCount)
	for r := range out {
		out[r] = flat[r*len(schema):][:len(schema):len(schema)]
	}
	pos := 0
	for c, col := range schema {
		w := widths[c]
		for r := range recordCount {
			b := stream[pos+r*w : pos+(r+1)*w]
			var fv fieldValue
			switch col.Type {
			case ColumnInteger:
				switch col.Size {
				case 2:
					raw := binary.LittleEndian.Uint16(b)
					fv.num = uint32(int16(raw ^ 0x8000))
					fv.null = raw == 0
				case 4:
					raw := binary.LittleEndian.Uint32(b)
					fv.num = raw ^ 0x80000000
					fv.null = raw == 0
				}
			case ColumnText:
				id := uint32(b[0]) | uint32(b[1])<<8
				if longRefs {
					id |= uint32(b[2]) << 16
				}
				fv.num = id
				fv.null = id == 0
			case ColumnBinary:
				raw := binary.LittleEndian.Uint16(b)
				fv.num = uint32(int16(raw ^ 0x8000))
				fv.null = raw == 0
			}
			out[r][c] = fv
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
	if _, err := io.ReadFull(s.Open(), buf); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}
