package msidb_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestColumns(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `A` (Id INT NOT NULL, B INT PRIMARY KEY Id)")
	mustExec(t, db, "CREATE TABLE `B` (Id INT NOT NULL, Label CHAR(8) PRIMARY KEY Id)")

	// SELECT * over a join expands FROM order, duplicate names kept.
	rows, err := db.Query("SELECT * FROM A, B WHERE A.B = B.Id")
	if err != nil {
		t.Fatal(err)
	}
	cols := rows.Columns()
	rows.Close()
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Table + "." + c.Name
	}
	if diff := cmp.Diff([]string{"A.Id", "A.B", "B.Id", "B.Label"}, names); diff != "" {
		t.Errorf("SELECT * expansion (-want +got):\n%s", diff)
	}
}

func TestQueryRowNoRows(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)")
	var id int
	err := db.QueryRow("SELECT Id FROM `T` WHERE Id = ?", 7).Scan(&id)
	if !errors.Is(err, msidb.ErrNoRows) {
		t.Errorf("QueryRow no rows err = %v, want ErrNoRows", err)
	}
}

func TestScanRejectsBadDestinations(t *testing.T) {
	tests := []struct {
		name string
		dest []any
		want string
	}{
		{"too few", []any{new(int)}, "destinations"},
		{"not a pointer", []any{new(int), 0}, "not a pointer"},
		{"nil pointer", []any{new(int), (*int)(nil)}, "nil"},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, N INT PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id, N) VALUES (1, 7)")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := db.QueryRow("SELECT Id, N FROM `T`").Scan(test.dest...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("Scan = %v, want %q", err, test.want)
			}
		})
	}

	var a, b int
	if err := db.QueryRow("SELECT Id, N FROM `T`").Scan(&a, &b); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if a != 1 || b != 7 {
		t.Errorf("got Id=%d N=%d, want 1 and 7", a, b)
	}
}

func TestOpenRowsUnderMutation(t *testing.T) {
	tempB := []string{
		"CREATE TABLE `T` (A INT NOT NULL PRIMARY KEY A)",
		"INSERT INTO `T` (A) VALUES (1)",
		"ALTER TABLE `T` ADD B INT TEMPORARY HOLD",
		"UPDATE `T` SET B = 7",
	}
	tests := []struct {
		name   string
		setup  []string
		query  string
		during []string
		want   [][]any
	}{
		{
			name: "delete reads as null",
			setup: []string{
				"CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)",
				"INSERT INTO `T` (Id) VALUES (1)",
				"INSERT INTO `T` (Id) VALUES (2)",
				"INSERT INTO `T` (Id) VALUES (3)",
			},
			query:  "SELECT Id FROM `T`",
			during: []string{"DELETE FROM `T` WHERE Id = 2"},
			want:   [][]any{{1}, {nil}, {3}},
		},
		{
			name: "delete under join nulls only its half",
			setup: []string{
				"CREATE TABLE `A` (K CHAR(8), V INT NOT NULL PRIMARY KEY K)",
				"CREATE TABLE `B` (K CHAR(8), W INT NOT NULL PRIMARY KEY K)",
				"INSERT INTO `A` (K, V) VALUES ('x', 1)",
				"INSERT INTO `A` (K, V) VALUES (NULL, 3)",
				"INSERT INTO `B` (K, W) VALUES ('x', 10)",
				"INSERT INTO `B` (K, W) VALUES (NULL, 30)",
			},
			query:  "SELECT A.K, A.V, B.W FROM `A`, `B` WHERE A.K = B.K",
			during: []string{"DELETE FROM `A` WHERE V = 1"},
			want:   [][]any{{nil, 3, 30}, {nil, nil, 10}},
		},
		{
			name: "add and update keep values live",
			setup: []string{
				"CREATE TABLE `T` (Id INT NOT NULL, S CHAR(8) PRIMARY KEY Id)",
				"INSERT INTO `T` (Id, S) VALUES (1, 'v1')",
				"INSERT INTO `T` (Id, S) VALUES (2, 'v2')",
			},
			query:  "SELECT Id, S FROM `T`",
			during: []string{"ALTER TABLE `T` ADD X INT", "UPDATE `T` SET S = 'u2' WHERE Id = 2"},
			want:   [][]any{{1, "v1"}, {2, "u2"}},
		},
		{
			name:   "free reads as null",
			setup:  tempB,
			query:  "SELECT A, B FROM `T`",
			during: []string{"ALTER TABLE `T` FREE"},
			want:   [][]any{{1, nil}},
		},
		{
			name:   "free then add reads as null",
			setup:  tempB,
			query:  "SELECT A, B FROM `T`",
			during: []string{"ALTER TABLE `T` FREE", "ALTER TABLE `T` ADD D INT", "UPDATE `T` SET D = 42"},
			want:   [][]any{{1, nil}},
		},
		{
			name:   "free then add same name reads as null",
			setup:  tempB,
			query:  "SELECT A, B FROM `T`",
			during: []string{"ALTER TABLE `T` FREE", "ALTER TABLE `T` ADD B INT", "UPDATE `T` SET B = 42"},
			want:   [][]any{{1, nil}},
		},
		{
			name:   "free two then add one reads as null",
			setup:  append(tempB, "ALTER TABLE `T` ADD C INT TEMPORARY HOLD", "UPDATE `T` SET C = 7"),
			query:  "SELECT A, C FROM `T`",
			during: []string{"ALTER TABLE `T` FREE", "ALTER TABLE `T` FREE", "ALTER TABLE `T` ADD D INT", "UPDATE `T` SET D = 42"},
			want:   [][]any{{1, nil}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, _ := newDB(t)
			for _, q := range test.setup {
				mustExec(t, db, q)
			}
			rows, err := db.Query(test.query)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for _, q := range test.during {
				mustExec(t, db, q)
			}
			var got [][]any
			for rows.Next() {
				vals, err := rows.Values()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, vals)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
