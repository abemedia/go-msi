package msidb_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-msi/msidb"
)

const benchRows = 1000

var benchKinds = []string{"alpha", "beta", "gamma", "delta"}

// benchDB returns a database with one table T of benchRows records: Id is
// the primary key (0..benchRows-1), N is a small integer range, S cycles a
// few strings (so DISTINCT and string filters are meaningful).
func benchDB(b *testing.B) *msidb.Database {
	b.Helper()
	db, err := msidb.Create(filepath.Join(b.TempDir(), "t.msi"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	mustExec(b, db, "CREATE TABLE `T` (Id INT NOT NULL, N INT, S CHAR(32) PRIMARY KEY Id)")
	for i := range benchRows {
		mustExec(b, db, "INSERT INTO `T` (Id, N, S) VALUES (?, ?, ?)", i, i%500, benchKinds[i%len(benchKinds)])
	}
	return db
}

func benchQuery(b *testing.B, db *msidb.Database, sql string, args ...any) {
	b.Helper()
	rows, err := db.Query(sql, args...)
	if err != nil {
		b.Fatal(err)
	}
	for rows.Next() {
		if _, err := rows.Values(); err != nil {
			b.Fatal(err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkSelectScanAll(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id, N, S FROM `T`")
	}
}

func BenchmarkSelectProject(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id FROM `T`")
	}
}

func BenchmarkSelectWhereInt(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id FROM `T` WHERE N >= ?", 250)
	}
}

func BenchmarkSelectWhereString(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id FROM `T` WHERE S = ?", "beta")
	}
}

func BenchmarkSelectWhereAnd(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id FROM `T` WHERE N >= ? AND S = ?", 100, "gamma")
	}
}

func BenchmarkSelectPoint(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT N, S FROM `T` WHERE Id = ?", benchRows/2)
	}
}

func BenchmarkQueryRow(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		var n int
		if err := db.QueryRow("SELECT N FROM `T` WHERE Id = ?", benchRows/2).Scan(&n); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelectOrderBy(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT Id FROM `T` ORDER BY N")
	}
}

func BenchmarkSelectDistinct(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		benchQuery(b, db, "SELECT DISTINCT S FROM `T`")
	}
}

func BenchmarkSelectJoin(b *testing.B) {
	db, err := msidb.Create(filepath.Join(b.TempDir(), "j.msi"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	mustExec(b, db, "CREATE TABLE `A` (Id INT NOT NULL, B INT PRIMARY KEY Id)")
	mustExec(b, db, "CREATE TABLE `D` (Id INT NOT NULL, Label CHAR(16) PRIMARY KEY Id)")
	for i := range 200 {
		mustExec(b, db, "INSERT INTO `A` (Id, B) VALUES (?, ?)", i, i%40)
	}
	for i := range 40 {
		mustExec(b, db, "INSERT INTO `D` (Id, Label) VALUES (?, ?)", i, "label")
	}
	for b.Loop() {
		benchQuery(b, db, "SELECT A.Id, D.Label FROM A, D WHERE A.B = D.Id")
	}
}

func BenchmarkSelectJoinThree(b *testing.B) {
	db, err := msidb.Create(filepath.Join(b.TempDir(), "j.msi"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	mustExec(b, db, "CREATE TABLE `A` (Id INT NOT NULL, B INT PRIMARY KEY Id)")
	mustExec(b, db, "CREATE TABLE `B` (Id INT NOT NULL, C INT PRIMARY KEY Id)")
	mustExec(b, db, "CREATE TABLE `C` (Id INT NOT NULL, Label CHAR(16) PRIMARY KEY Id)")
	for i := range 200 {
		mustExec(b, db, "INSERT INTO `A` (Id, B) VALUES (?, ?)", i, i+1)
		mustExec(b, db, "INSERT INTO `B` (Id, C) VALUES (?, ?)", i, i+1)
		mustExec(b, db, "INSERT INTO `C` (Id, Label) VALUES (?, ?)", i, "label")
	}
	for b.Loop() {
		benchQuery(b, db, "SELECT A.Id, B.Id, C.Id FROM A, B, C WHERE A.Id = 7 AND A.B = B.Id AND B.C = C.Id")
	}
}

func BenchmarkSelectBinary(b *testing.B) {
	db := benchDB(b)
	mustExec(b, db, "CREATE TABLE `Bin` (Id INT NOT NULL, Data OBJECT PRIMARY KEY Id)")
	mustExec(b, db, "INSERT INTO `Bin` (Id, Data) VALUES (?, ?)", 1, []byte("some payload bytes"))
	for b.Loop() {
		rows, err := db.Query("SELECT Data FROM `Bin` WHERE Id = ?", 1)
		if err != nil {
			b.Fatal(err)
		}
		if !rows.Next() {
			b.Fatal("no row")
		}
		var rs io.ReadSeeker
		if err := rows.Scan(&rs); err != nil {
			b.Fatal(err)
		}
		rows.Close()
	}
}

func BenchmarkInsertDelete(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		mustExec(b, db, "INSERT INTO `T` (Id, N, S) VALUES (?, ?, ?)", benchRows, 1, "x")
		mustExec(b, db, "DELETE FROM `T` WHERE Id = ?", benchRows)
	}
}

func BenchmarkStreamInsertDelete(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		mustExec(b, db, "INSERT INTO _Streams (Name, Data) VALUES (?, ?)", "bench", []byte("stream payload"))
		mustExec(b, db, "DELETE FROM _Streams WHERE Name = ?", "bench")
	}
}

func BenchmarkUpdate(b *testing.B) {
	db := benchDB(b)
	for i := 0; b.Loop(); i++ {
		mustExec(b, db, "UPDATE `T` SET N = ? WHERE Id = ?", i%500, benchRows/2)
	}
}

func BenchmarkCreateDrop(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		mustExec(b, db, "CREATE TABLE `Tmp` (Id INT NOT NULL, V CHAR(8) PRIMARY KEY Id)")
		mustExec(b, db, "DROP TABLE `Tmp`")
	}
}

func BenchmarkCreateAlterDrop(b *testing.B) {
	db := benchDB(b)
	for b.Loop() {
		mustExec(b, db, "CREATE TABLE `Tmp` (Id INT NOT NULL PRIMARY KEY Id)")
		mustExec(b, db, "ALTER TABLE `Tmp` ADD Extra CHAR(8)")
		mustExec(b, db, "DROP TABLE `Tmp`")
	}
}

func BenchmarkOpen(b *testing.B) {
	src, err := os.ReadFile("../testdata/hello.msi")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "hello.msi")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		db, err := msidb.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		db.Close()
	}
}

func BenchmarkRoundtrip(b *testing.B) {
	src, err := os.ReadFile("../testdata/hello.msi")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "hello.msi")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		db, err := msidb.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		msidb.ForcePersist(db)
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
