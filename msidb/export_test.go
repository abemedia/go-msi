package msidb

var (
	PackType   = packType
	UnpackType = unpackType
)

// ForcePersist makes the next [Database.Close] persist db even when unchanged.
func ForcePersist(db *Database) error { return db.markDirty() }
