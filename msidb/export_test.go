package msidb

var (
	PackType   = packType
	UnpackType = unpackType
	ErrClosed  = errClosed
)

// ForcePersist makes the next [Database.Close] persist db even when unchanged.
func ForcePersist(db *Database) {
	db.dirty = true
}
