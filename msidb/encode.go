package msidb

import (
	"fmt"
	"io"

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
	if err := e.writeStreamBytes(tableMarker+stringpoolName, poolData); err != nil {
		return err
	}
	return e.writeStreamBytes(tableMarker+stringdataName, dataData)
}

// writeSchemas emits _Tables and _Columns.
func (e *encoder) writeSchemas() error {
	tablesRecords := make([][]fieldValue, 0, len(e.db.tables))
	var columnsRecords [][]fieldValue
	for t := range e.db.Tables() {
		if t.name == systemTableStreams {
			continue
		}
		tableID, _ := e.db.pool.LookupID(t.name)
		tablesRecords = append(tablesRecords, []fieldValue{{num: tableID}})
		for j, col := range t.columns {
			colID, _ := e.db.pool.LookupID(col.Name)
			columnsRecords = append(columnsRecords, []fieldValue{
				{num: tableID},
				{num: uint32(j + 1)},
				{num: colID},
				{num: packType(col)},
			})
		}
	}

	longRefs := e.db.pool.LongRefs()
	tablesData, err := encodeTable(tablesRecords, schemaTables, longRefs)
	if err != nil {
		return fmt.Errorf("encode _Tables: %w", err)
	}
	if err := e.writeStreamBytes(tableMarker+systemTableTables, tablesData); err != nil {
		return err
	}
	columnsData, err := encodeTable(columnsRecords, schemaColumns, longRefs)
	if err != nil {
		return fmt.Errorf("encode _Columns: %w", err)
	}
	return e.writeStreamBytes(tableMarker+systemTableColumns, columnsData)
}

func (e *encoder) writeTables() error {
	longRefs := e.db.pool.LongRefs()
	for t := range e.db.Tables() {
		if t.name == systemTableStreams {
			continue
		}
		records := make([][]fieldValue, len(t.records))
		for i, r := range t.records {
			records[i] = r.fields
		}
		stream, err := encodeTable(records, t.columns, longRefs)
		if err != nil {
			return fmt.Errorf("table %s: %w", t.name, err)
		}
		if err := e.writeStreamBytes(tableMarker+t.name, stream); err != nil {
			return fmt.Errorf("write table %s: %w", t.name, err)
		}
		for _, r := range t.records {
			for ci, c := range t.columns {
				if c.Type != ColumnBinary {
					continue
				}
				src := r.fields[ci].bin
				if src == nil {
					continue
				}
				name, err := r.binaryStreamName()
				if err != nil {
					return fmt.Errorf("table %s col %s: %w", t.name, c.Name, err)
				}
				if err := e.writeStream(tableMarker+name, src); err != nil {
					return fmt.Errorf("table %s col %s: %w", t.name, c.Name, err)
				}
			}
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
		nameFV := r.fields[0]
		name, ok := e.db.pool.Lookup(nameFV.num)
		if !ok {
			return fmt.Errorf("_Streams: unknown string ID %d", nameFV.num)
		}
		src := r.fields[1].bin
		if src == nil {
			continue
		}
		if err := e.writeStream(name, src); err != nil {
			return fmt.Errorf("write stream %s: %w", name, err)
		}
	}
	return nil
}

// writeStreamBytes writes data as a new CFB stream named name.
func (e *encoder) writeStreamBytes(name string, data []byte) error {
	sw, err := e.cw.CreateStream(streamname.Encode(name))
	if err != nil {
		return err
	}
	if _, err := sw.Write(data); err != nil {
		sw.Close()
		return err
	}
	return sw.Close()
}

// writeStream copies src into a new CFB stream named name and closes both.
func (e *encoder) writeStream(name string, src streamSource) error {
	sw, err := e.cw.CreateStream(streamname.Encode(name))
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(sw, src.open(), e.buf); err != nil {
		sw.Close()
		return err
	}
	if err := sw.Close(); err != nil {
		return err
	}
	return src.close()
}

// encodeTable serialises per-record field slices to a column-major
// stream. Binary fields encode as a 0/1 presence marker only.
func encodeTable(records [][]fieldValue, schema []Column, longRefs bool) ([]byte, error) {
	widths, recordSize := columnWidths(schema, longRefs)
	if recordSize == 0 || len(records) == 0 {
		return nil, nil
	}
	stream := make([]byte, len(records)*recordSize)
	pos := 0
	for c, col := range schema {
		w := widths[c]
		for r, rec := range records {
			fv := rec[c]
			if fv.null {
				continue
			}
			dst := stream[pos+r*w : pos+(r+1)*w]
			switch col.Type {
			case ColumnInteger:
				switch col.Size {
				case 2:
					v := int32(fv.num)
					if v < -32767 || v > 32767 {
						return nil, fmt.Errorf("record %d col %s: value %d out of range for int(2)", r, col.Name, v)
					}
					raw := uint16(v) ^ 0x8000
					dst[0] = byte(raw)
					dst[1] = byte(raw >> 8)
				case 4:
					raw := fv.num ^ 0x80000000
					dst[0] = byte(raw)
					dst[1] = byte(raw >> 8)
					dst[2] = byte(raw >> 16)
					dst[3] = byte(raw >> 24)
				}
			case ColumnText:
				id := fv.num
				dst[0] = byte(id)
				dst[1] = byte(id >> 8)
				if longRefs {
					dst[2] = byte(id >> 16)
				} else if id>>16 != 0 {
					return nil, fmt.Errorf("record %d col %s: string ID %d does not fit a short string ref", r, col.Name, id)
				}
			case ColumnBinary:
				raw := uint16(1) ^ 0x8000
				dst[0] = byte(raw)
				dst[1] = byte(raw >> 8)
			}
		}
		pos += len(records) * w
	}
	return stream, nil
}
