package store

// Boolean columns are stored as INTEGER 0/1. The pure-Go SQLite driver returns
// int64 for those columns, which does not scan into *bool, so booleans are
// bound and scanned through these helpers.

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool { return i != 0 }
